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

func TestSessionBatchReuseAndMissing(t *testing.T) {
	s := NewSession()
	defer s.Close()
	var calls [][]string
	resolve := func(_ context.Context, names []string) (jobdb.SecretResolveResult, error) {
		calls = append(calls, append([]string(nil), names...))
		out := jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{}}
		for _, n := range names {
			if n != "missing" {
				out.Secrets[n] = jobdb.SecretValue{Value: n + "-private\n"}
			}
		}
		out.Secrets["empty"] = jobdb.SecretValue{Value: ""}
		return out, nil
	}
	for i := 0; i < 10; i++ {
		require.NoError(t, s.Prepare(context.Background(), []string{"token"}, []string{"future", "empty", "missing"}, resolve))
	}
	require.Len(t, calls, 1)
	require.Equal(t, []string{"token", "future", "empty", "missing"}, calls[0])
	require.NoError(t, s.Prepare(context.Background(), []string{"future", "empty"}, nil, resolve))
	require.Len(t, calls, 1)
	input := map[string]any{"token": Reference{"token"}, "empty": Reference{"empty"}}
	out, err := s.Materialize(input)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"token": "token-private\n", "empty": ""}, out)
	require.IsType(t, Reference{}, input["token"])
	require.ErrorContains(t, s.Prepare(context.Background(), []string{"missing"}, nil, resolve), "required secret unavailable")
	require.Error(t, s.Prepare(context.Background(), []string{"missing"}, nil, resolve))
	require.Len(t, calls, 3)
	require.NoError(t, s.Prepare(context.Background(), []string{"later"}, nil, resolve))
	require.Len(t, calls, 4)
	require.Error(t, s.CheckOutput(map[string]any{"echo": "token-private\n"}))
	require.Equal(t, "failure: [REDACTED]", s.Error(fmt.Errorf("failure: %w", errors.New("token-private\n"))).Error())
	_, err = json.Marshal(s)
	require.Error(t, err)
}

func TestSessionSpeculativeFailureFallsBack(t *testing.T) {
	s := NewSession()
	defer s.Close()
	calls := 0
	resolve := func(_ context.Context, names []string) (jobdb.SecretResolveResult, error) {
		calls++
		if len(names) > 1 {
			return jobdb.SecretResolveResult{}, errors.New("private provider details")
		}
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "value"}}}, nil
	}
	require.NoError(t, s.Prepare(context.Background(), []string{"token"}, []string{"future"}, resolve))
	require.Equal(t, 2, calls)
}

func TestSessionConcurrentFillAndExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := NewSession()
		defer s.Close()
		calls := 0
		entered := make(chan struct{})
		release := make(chan struct{})
		resolve := func(_ context.Context, _ []string) (jobdb.SecretResolveResult, error) {
			calls++
			if calls == 1 {
				close(entered)
				<-release
			}
			expiry := time.Now().Add(time.Minute)
			return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "value", ExpiresAt: &expiry}}}, nil
		}
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Go(func() {
				if err := s.Prepare(context.Background(), []string{"token"}, nil, resolve); err != nil {
					t.Error(err)
				}
			})
		}
		<-entered
		synctest.Wait()
		close(release)
		wg.Wait()
		require.Equal(t, 1, calls)
		time.Sleep(time.Minute)
		require.False(t, s.Ready([]string{"token"}))
		require.NoError(t, s.Prepare(context.Background(), []string{"token"}, nil, resolve))
		require.Equal(t, 2, calls)
	})
}

func TestSessionCloseDuringFillAndExecutionIsolation(t *testing.T) {
	m := NewSessions()
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	s, err := m.Open(key)
	require.NoError(t, err)
	_, err = m.Open(key)
	require.Error(t, err)
	err = s.Prepare(context.Background(), []string{"token"}, nil, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		m.Close(key)
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "private"}}}, nil
	})
	require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
	require.False(t, s.Ready([]string{"token"}))
	require.Empty(t, s.values)
	next, err := m.Open(key)
	require.NoError(t, err)
	defer m.Close(key)
	require.NotSame(t, s, next)
	require.False(t, next.Ready([]string{"token"}))
}

type hiddenSecretCause struct{ cause error }

func (hiddenSecretCause) Error() string   { return "safe outer error" }
func (e hiddenSecretCause) Unwrap() error { return e.cause }

func TestSessionLeaseLossDropsCacheAndErrorChains(t *testing.T) {
	s := NewSession()
	defer s.Close()
	ctx := context.Background()
	require.NoError(t, s.Prepare(ctx, []string{"token"}, nil, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "private-value"}}}, nil
	}))
	err := s.Error(hiddenSecretCause{errors.New("private-value")})
	require.Equal(t, "safe outer error", err.Error())
	require.Nil(t, errors.Unwrap(err))
	err = s.Prepare(ctx, []string{"new"}, nil, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{}, jobdb.ErrExecutionLeaseLost
	})
	require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
	require.False(t, s.Ready([]string{"token"}))
	require.Empty(t, s.values)
	require.Empty(t, s.masks)
	_, err = s.Materialize(Reference{Name: "token"})
	require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
}
