package secrets

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"log/slog"
	"testing"

	"github.com/colony-2/c2j/pkg/logging"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type stringerSecret string

func (s stringerSecret) String() string { return string(s) }

func TestLogMasksValuesAndPreservesGroups(t *testing.T) {
	var out bytes.Buffer
	prev := slog.Default()
	previousWriter, previousFlags := log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	defer func() { slog.SetDefault(prev); log.SetOutput(previousWriter); log.SetFlags(previousFlags) }()
	sessions := NewSessions()
	key := jobdb.JobKey{TenantId: "t", JobId: "j"}
	redactor, err := sessions.Open(key)
	require.NoError(t, err)
	defer sessions.Close(key)
	s := redactor.NewTask()
	defer s.Close()
	require.NoError(t, s.Prepare(context.Background(), []string{"token"}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "private-token\n"}}}, nil
	}))
	s.Close() // Masking survives completion of the task that fetched the value.
	slog.Default().With("root", "yes").WithGroup("nested").With("saved", "private-token\n").Info("private-token\n", "error", fmt.Errorf("failed: %s", "private-token\n"), "stringer", stringerSecret("private-token\n"), "map", map[string]string{"value": "private-token\n"})
	require.NotContains(t, out.String(), "private-token")
	require.Contains(t, out.String(), `"root":"yes","nested":`)
	require.Contains(t, out.String(), "[REDACTED]")
}

func TestInjectedSelectiveLoggerMasksFieldsCapturedBeforeSecretSetup(t *testing.T) {
	var out bytes.Buffer
	previous, writer, flags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() { slog.SetDefault(previous); log.SetOutput(writer); log.SetFlags(flags) })
	base, err := logging.New(&out, logging.Options{Format: "json", Debug: []logging.Selector{{Field: "component", Value: "worker"}}})
	require.NoError(t, err)
	logger := MaskLogger(base)
	require.Same(t, previous, slog.Default(), "wrapping an injected logger does not change globals")
	child := logger.With("component", "worker").WithGroup("payload").With("captured", "late-private-token")
	slog.SetDefault(logger)
	sessions := NewSessions()
	key := jobdb.JobKey{TenantId: "logging", JobId: "injected"}
	redactor, err := sessions.Open(key)
	require.NoError(t, err)
	defer sessions.Close(key)
	session := redactor.NewTask()
	defer session.Close()
	require.NoError(t, session.Prepare(context.Background(), []string{"token"}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "late-private-token"}}}, nil
	}))
	child.Debug("late-private-token", "error", fmt.Errorf("failed: late-private-token"))
	logger.Debug("unrelated detail")
	log.Print("legacy late-private-token")
	require.NotContains(t, out.String(), "late-private-token")
	require.NotContains(t, out.String(), "unrelated detail")
	require.Contains(t, out.String(), `"level":"DEBUG"`)
	require.Contains(t, out.String(), `"captured":"[REDACTED]"`)
	require.Contains(t, out.String(), "legacy [REDACTED]")
}
