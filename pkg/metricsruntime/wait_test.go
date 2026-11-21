package metricsruntime

import (
	"context"
	"errors"
	"github.com/colony-2/c2j/pkg/extensionwait"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExtensionWaitMetricsDiscard(t *testing.T) {
	for _, joined := range []bool{false, true} {
		_, timing := Begin(context.Background(), "wait", nil)
		err := extensionwait.New(extensionwait.Request{Version: 1})
		if joined {
			err = errors.Join(err, errors.New("cleanup"))
		}
		out, got := FinishTask(timing, task.OutputKindActivityInvocationOutput, jobdb.NewTaskDataOrPanic(map[string]any{"discard": "marker"}), err)
		require.Nil(t, out)
		require.Equal(t, err, got)
		processSampler.mu.Lock()
		require.Empty(t, processSampler.active)
		processSampler.mu.Unlock()
	}
}
