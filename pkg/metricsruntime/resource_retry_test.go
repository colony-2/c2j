package metricsruntime

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/internal/resourceretry"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestResourceRetryMetricsDiscard(t *testing.T) {
	_, timing := Begin(context.Background(), "resource", nil)
	signal := resourceretry.Restart("local_spawn", "launch")
	out, err := FinishTask(timing, task.OutputKindActivityInvocationOutput, jobdb.NewTaskDataOrPanic(map[string]any{"discard": true}), signal)
	require.Nil(t, out)
	require.Equal(t, signal, err)
	processSampler.mu.Lock()
	defer processSampler.mu.Unlock()
	require.Empty(t, processSampler.active, "resource waits must stop CPU sampling without publishing metrics")
}
