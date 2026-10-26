package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestTasksResolveIndependentlyAndShareOnlyRedaction(t *testing.T) {
	sessions := NewSessions()
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	redactor, err := sessions.Open(key)
	require.NoError(t, err)
	defer sessions.Close(key)
	calls := 0
	resolve := func(_ context.Context, names []string) (jobdb.SecretResolveResult, error) {
		calls++
		require.Equal(t, []string{"token"}, names)
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: fmt.Sprintf("private-%d", calls)}}}, nil
	}
	first := redactor.NewTask()
	require.NoError(t, first.Prepare(context.Background(), []string{"token", "token"}, resolve))
	value, err := first.Materialize(Reference{Name: "token"})
	require.NoError(t, err)
	require.Equal(t, "private-1", value)
	first.Close()
	require.Empty(t, first.values)
	require.Equal(t, "[REDACTED]", redactor.Redact("private-1"), "completed task values still need masking")
	second := redactor.NewTask()
	defer second.Close()
	require.False(t, second.Ready([]string{"token"}))
	require.NoError(t, second.Prepare(context.Background(), []string{"token"}, resolve))
	value, err = second.Materialize(Reference{Name: "token"})
	require.NoError(t, err)
	require.Equal(t, "private-2", value)
	require.Equal(t, 2, calls)
	require.Equal(t, "[REDACTED] [REDACTED]", second.Redact("private-1 private-2"))
	require.Error(t, second.CheckOutput(map[string]any{"echo": "private-1"}))
	_, err = json.Marshal(redactor)
	require.Error(t, err)
	_, err = json.Marshal(second)
	require.Error(t, err)
}

func TestPreparationAlwaysRefreshesAndClearsFailedValues(t *testing.T) {
	s := NewSession()
	defer s.Close()
	calls := 0
	resolve := func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		calls++
		if calls == 1 {
			return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "private"}}}, nil
		}
		return jobdb.SecretResolveResult{}, nil
	}
	require.NoError(t, s.Prepare(context.Background(), []string{"token"}, resolve))
	require.ErrorContains(t, s.Prepare(context.Background(), []string{"token"}, resolve), "required secret unavailable")
	require.Equal(t, 2, calls)
	require.Empty(t, s.values)
	require.Equal(t, "[REDACTED]", s.Redact("private"))
}

func TestTaskBatchesBoundedAndPartialFailureClearsValues(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s := NewSession()
		var names []string
		for i := 0; i < 130; i++ {
			names = append(names, fmt.Sprintf("token%d", i))
		}
		calls := 0
		err := s.Prepare(context.Background(), names, func(_ context.Context, batch []string) (jobdb.SecretResolveResult, error) {
			calls++
			require.LessOrEqual(t, len(batch), 64)
			if fail && calls == 2 {
				return jobdb.SecretResolveResult{}, errors.New("private provider detail")
			}
			result := jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{}}
			for _, name := range batch {
				result.Secrets[name] = jobdb.SecretValue{Value: "private-" + name}
			}
			return result, nil
		})
		if fail {
			require.ErrorContains(t, err, "secret resolution unavailable")
			require.Equal(t, 2, calls, "no speculative fallback or retry")
			require.Empty(t, s.values)
			require.Equal(t, "[REDACTED]", s.Redact("private-token0"))
		} else {
			require.NoError(t, err)
			require.Equal(t, 3, calls)
			require.Len(t, s.values, 130)
		}
		s.Close()
		require.Empty(t, s.values)
	}
}

func TestConcurrentTasksHaveSeparateValues(t *testing.T) {
	redactor := newRedactor()
	defer redactor.Close()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Go(func() {
			task := redactor.NewTask()
			defer task.Close()
			want := fmt.Sprintf("private-%d", i)
			err := task.Prepare(context.Background(), []string{"token"}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
				return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: want}}}, nil
			})
			if err != nil {
				t.Error(err)
				return
			}
			got, err := task.Materialize(Reference{Name: "token"})
			if err != nil || got != want {
				t.Errorf("task secret isolation failed")
			}
		})
	}
	wg.Wait()
	for i := 0; i < 10; i++ {
		require.Equal(t, "[REDACTED]", redactor.Redact(fmt.Sprintf("private-%d", i)))
	}
}

func TestTaskSecretExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := NewSession()
		defer s.Close()
		require.NoError(t, s.Prepare(context.Background(), []string{"token"}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
			expiry := time.Now().Add(time.Minute)
			return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "private", ExpiresAt: &expiry}}}, nil
		}))
		time.Sleep(time.Minute)
		_, err := s.Materialize(Reference{Name: "token"})
		require.ErrorContains(t, err, "required secret unavailable")
	})
}

func TestExecutionCloseDuringResolutionAndRestart(t *testing.T) {
	m := NewSessions()
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	redactor, err := m.Open(key)
	require.NoError(t, err)
	task := redactor.NewTask()
	defer task.Close()
	err = task.Prepare(context.Background(), []string{"token"}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		m.Close(key)
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "private"}}}, nil
	})
	require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
	require.Empty(t, task.values)
	next, err := m.Open(key)
	require.NoError(t, err)
	defer m.Close(key)
	require.NotSame(t, redactor, next)
	require.Empty(t, next.masks)
}

type hiddenSecretCause struct{ cause error }

func (hiddenSecretCause) Error() string   { return "safe outer error" }
func (e hiddenSecretCause) Unwrap() error { return e.cause }

func TestTaskLeaseLossClearsValuesAndPreservesRedaction(t *testing.T) {
	s := NewSession()
	defer s.Close()
	ctx := context.Background()
	require.NoError(t, s.Prepare(ctx, []string{"token"}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "private-value"}}}, nil
	}))
	err := s.Error(hiddenSecretCause{errors.New("private-value")})
	require.Equal(t, "safe outer error", err.Error())
	require.Nil(t, errors.Unwrap(err))
	err = s.Prepare(ctx, []string{"new"}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{}, jobdb.ErrExecutionLeaseLost
	})
	require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
	require.Empty(t, s.values)
	require.Equal(t, "[REDACTED]", s.Redact("private-value"))
	_, err = s.Materialize(Reference{Name: "token"})
	require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
}

func TestExecutionRecoveryBeforePriorCleanup(t *testing.T) {
	m := NewSessions()
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	priorCtx, cancel := context.WithCancel(context.Background())
	prior, err := m.OpenExecution(priorCtx, key)
	require.NoError(t, err)
	task := prior.NewTask()
	require.NoError(t, task.Prepare(priorCtx, []string{"token"}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "prior-secret"}}}, nil
	}))
	cancel()
	next, err := m.OpenExecution(context.Background(), key)
	require.NoError(t, err, "resume must not wait for cancelled callback cleanup")
	defer m.CloseExecution(key, next)
	require.NotSame(t, prior, next)
	require.Empty(t, next.masks)
	require.Nil(t, m.GetExecution(priorCtx, key), "cancelled tasks cannot acquire the new activation's redactor")
	m.CloseExecution(key, prior)
	require.Same(t, next, m.Get(key), "late cleanup must not close the new activation")
	require.False(t, next.isClosed())
	require.Equal(t, "[REDACTED]", task.Redact("prior-secret"), "late task diagnostics remain redacted")
	_, registered := activeMasks.Load(prior)
	require.True(t, registered, "late task logs remain masked")
	_, err = task.Materialize(Reference{Name: "token"})
	require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
	task.Close()
	task.Close() // Cleanup is idempotent.
	require.Empty(t, prior.masks)
	_, registered = activeMasks.Load(prior)
	require.False(t, registered)
}

func TestExecutionHandoffWaitsForPreviousActivation(t *testing.T) {
	for _, release := range []string{"cancel", "close", "close execution"} {
		t.Run(release, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := NewSessions()
				key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
				priorCtx, cancel := context.WithCancel(context.Background())
				defer cancel()
				prior, err := m.OpenExecution(priorCtx, key)
				require.NoError(t, err)
				defer m.CloseExecution(key, prior)
				require.NoError(t, prior.Register("prior-secret"))
				nextReady := make(chan *Redactor, 1)
				go func() {
					next, err := m.OpenExecution(context.Background(), key)
					if err != nil {
						t.Error(err)
					}
					nextReady <- next
				}()
				synctest.Wait()
				require.Empty(t, nextReady, "the next activation must wait during handoff")
				require.Same(t, prior, m.Get(key))
				other := jobdb.JobKey{TenantId: "tenant", JobId: "other"}
				_, err = m.Open(other)
				require.NoError(t, err, "waiting must not block other jobs")
				m.Close(other)
				switch release {
				case "cancel":
					cancel()
				case "close":
					m.Close(key)
				case "close execution":
					m.CloseExecution(key, prior)
				}
				next := <-nextReady
				require.NotNil(t, next)
				defer m.CloseExecution(key, next)
				require.NotSame(t, prior, next)
				require.Empty(t, next.masks, "activations must not share secret state")
				m.CloseExecution(key, prior)
				require.Same(t, next, m.Get(key), "late cleanup must preserve the new activation")
			})
		})
	}
}

func TestExecutionHandoffWaitCanBeCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewSessions()
		key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
		prior, err := m.Open(key)
		require.NoError(t, err)
		defer m.CloseExecution(key, prior)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := m.OpenExecution(ctx, key)
			done <- err
		}()
		synctest.Wait()
		require.Empty(t, done)
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
		require.Same(t, prior, m.Get(key))
		require.False(t, prior.isClosed())
		_, err = m.OpenExecution(ctx, key)
		require.ErrorIs(t, err, context.Canceled, "already cancelled requests must not acquire a session")
	})
}

func TestExecutionHandoffSerializesMultipleWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewSessions()
		key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
		prior, err := m.Open(key)
		require.NoError(t, err)
		nextReady := make(chan *Redactor, 2)
		for range 2 {
			go func() {
				next, err := m.Open(key)
				if err != nil {
					t.Error(err)
				}
				nextReady <- next
			}()
		}
		synctest.Wait()
		require.Empty(t, nextReady)
		m.CloseExecution(key, prior)
		first := <-nextReady
		synctest.Wait()
		require.Empty(t, nextReady, "only one waiting activation may acquire the session")
		m.CloseExecution(key, first)
		second := <-nextReady
		require.NotSame(t, first, second)
		m.CloseExecution(key, first)
		require.Same(t, second, m.Get(key))
		m.CloseExecution(key, second)
		require.Nil(t, m.Get(key))
	})
}
