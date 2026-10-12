package secrets

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"log/slog"
	"testing"

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
	s, err := sessions.Open(key)
	require.NoError(t, err)
	defer sessions.Close(key)
	require.NoError(t, s.Prepare(context.Background(), []string{"token"}, nil, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "private-token\n"}}}, nil
	}))
	slog.Default().With("root", "yes").WithGroup("nested").With("saved", "private-token\n").Info("private-token\n", "error", fmt.Errorf("failed: %s", "private-token\n"), "stringer", stringerSecret("private-token\n"), "map", map[string]string{"value": "private-token\n"})
	require.NotContains(t, out.String(), "private-token")
	require.Contains(t, out.String(), `"root":"yes","nested":`)
	require.Contains(t, out.String(), "[REDACTED]")
}
