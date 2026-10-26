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

func TestTaskSessionsDoNotShareSecretsOrMasks(t *testing.T) {
	first, second := NewSession(), NewSession()
	defer first.Close()
	defer second.Close()
	for i, session := range []*Session{first, second} {
		value := fmt.Sprintf("private-%d", i)
		require.NoError(t, session.Prepare(context.Background(), []string{"token"}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
			return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: value}}}, nil
		}))
		got, err := session.Materialize(Reference{Name: "token"})
		require.NoError(t, err)
		require.Equal(t, value, got)
		_, err = json.Marshal(session)
		require.Error(t, err)
		_, err = json.Marshal(session.redactor)
		require.Error(t, err)
	}
	require.Equal(t, "[REDACTED] private-1", first.Redact("private-0 private-1"))
	require.Equal(t, "private-0 [REDACTED]", second.Redact("private-0 private-1"))
	first.Close()
	require.Empty(t, first.values)
	require.Empty(t, first.redactor.masks)
	require.Error(t, second.CheckOutput(map[string]any{"echo": "private-1"}))
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

func TestConcurrentSessionsHaveSeparateValueCaches(t *testing.T) {
	redactor := NewRedactor()
	defer redactor.Close()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Go(func() {
			task := redactor.NewSession()
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

func TestTaskCloseDuringResolution(t *testing.T) {
	redactor := NewRedactor()
	defer redactor.Close()
	task := redactor.NewSession()
	defer task.Close()
	err := task.Prepare(context.Background(), []string{"token"}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		redactor.Close()
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "private"}}}, nil
	})
	require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
	require.Empty(t, task.values)
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

func TestCancelledTaskRetainsRedactionUntilCleanup(t *testing.T) {
	prior := NewRedactor()
	task := prior.NewSession()
	require.NoError(t, task.Prepare(context.Background(), []string{"token"}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "prior-secret"}}}, nil
	}))
	prior.Close()
	next := NewSession()
	defer next.Close()
	require.Empty(t, next.redactor.masks)
	require.Equal(t, "[REDACTED]", task.Redact("prior-secret"))
	_, err := task.Materialize(Reference{Name: "token"})
	require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
	task.Close()
	task.Close()
	require.Empty(t, prior.masks)
	require.False(t, next.redactor.isClosed())
}
