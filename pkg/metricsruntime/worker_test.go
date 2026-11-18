package metricsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type setupFailureWorker struct{ panic bool }

func (setupFailureWorker) Name() string { return "setup-fixture" }
func (w setupFailureWorker) Run(wf.TaskContext, jobdb.TaskData) (jobdb.TaskData, error) {
	if w.panic {
		panic("setup panic")
	}
	return nil, errors.New("setup failed before body")
}

func TestWorkerClosesCPUOnFailureAndPanic(t *testing.T) {
	for _, panics := range []bool{false, true} {
		worker := WrapWorker(setupFailureWorker{panic: panics}, task.OutputKindActivityInvocationOutput)
		if panics {
			require.Panics(t, func() { _, _ = task.RunLocal(worker, context.Background(), wf.TaskContext{}, nil) })
		} else {
			out, err := task.RunLocal(worker, context.Background(), wf.TaskContext{}, nil)
			require.ErrorContains(t, err, "setup failed before body")
			require.NotNil(t, out, "runtime-only failure output can be committed")
			var env task.OutputEnvelope
			require.NoError(t, json.Unmarshal(out.GetDataOrPanic(), &env))
			require.Empty(t, env.Payload)
			require.Equal(t, "failed", env.Metrics.Runtime.Phases["preparation"])
			require.Equal(t, "not_started", env.Metrics.Runtime.Phases["execution"])
		}
		processSampler.mu.Lock()
		active, running := len(processSampler.active), processSampler.stop != nil
		processSampler.mu.Unlock()
		require.Zero(t, active)
		require.False(t, running, "a recovered worker panic must not leave the process sampler active")
	}
}
