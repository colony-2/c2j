package story_test

import (
	"bytes"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type storyLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *storyLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *storyLogBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}
func (b *storyLogBuffer) Reset() { b.mu.Lock(); defer b.mu.Unlock(); b.buf.Reset() }

func assertReplayDiagnostics(t *testing.T, data []byte, jobID string) {
	t.Helper()
	require.NotEmpty(t, data, "selected DEBUG replay summary must be observable")
	var summaries int
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var record map[string]any
		require.NoError(t, json.Unmarshal(line, &record))
		require.Equal(t, "DEBUG", record["level"], "historical failures are not current operational errors")
		require.Equal(t, jobID, record["job_id"])
		require.Contains(t, []string{"story", "restart_context"}, record["purpose"])
		require.NotEqual(t, "worker", record["component"], "replay cannot claim actual task execution")
		require.NotContains(t, record["msg"], "executed successfully")
		require.NotContains(t, record["msg"], "executing op")
		if record["event"] == "story.reconstruction.finished" || record["event"] == "restart_context.inspection.finished" {
			summaries++
			require.Equal(t, "succeeded", record["outcome"])
		}
	}
	require.Greater(t, summaries, 0)
}
