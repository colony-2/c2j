package compiler

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Record the declarations made by older compilers, including the implicit
// one-hour op timeout, without rewriting persisted history during replay.
type historicalContractContext struct {
	jobworkflow.ExecutionContext
	version uint32
}

func (c historicalContractContext) DoTask(call jobworkflow.TaskCall, build func(context.Context) (jobdb.TaskData, error)) (jobdb.TaskData, error) {
	if call.TaskType == "scope_probe:run" {
		call.ContractVersion = c.version
	}
	return c.ExecutionContext.DoTask(call, build)
}

func (c historicalContractContext) DoScope(site string, opts jobworkflow.ScopeOptions, body func(jobworkflow.ExecutionContext) (jobdb.JobData, error)) (jobdb.JobData, error) {
	if site == "op:a" {
		opts.TotalTimeout = time.Hour
	}
	return c.ExecutionContext.DoScope(site, opts, func(inner jobworkflow.ExecutionContext) (jobdb.JobData, error) {
		return body(historicalContractContext{ExecutionContext: inner, version: c.version})
	})
}

func TestOperationTaskContractReplay(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, tc := range []struct {
		name    string
		version uint32
		wantErr bool
	}{
		{"v1", 1, false},
		{"v2", 2, true},
		{"unknown version", 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, "sqlite")
			_, git := GenerateTestContext()
			task := &scopeProbeTask{calls: map[string]int{}, git: git}
			worker := probeWorker(t, probeRecipe(probeNode("a")))
			recording := worker
			recording.Execute = func(ctx jobworkflow.ExecutionContext, input jobdb.JobData) (jobdb.JobData, error) {
				return worker.Execute(historicalContractContext{ExecutionContext: ctx, version: tc.version}, input)
			}
			key := probeSubmit(t, rt, recording)
			outcome := probeRun(t, rt, key, recording, task)
			require.Equal(t, jobworkflow.JobRunCompleted, outcome.Status, "%v", outcome.JobError)
			before, err := rt.ListChapters(context.Background(), jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			err = probeReplay(t, rt, key, worker)
			if tc.wantErr {
				require.ErrorIs(t, err, jobworkflow.ErrWorkflowNotDeterministic)
			} else {
				require.NoError(t, err)
			}
			after, err := rt.ListChapters(context.Background(), jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			require.Equal(t, before, after, "replay must not rewrite history")
			require.Equal(t, map[string]int{"a": 1}, task.calls, "replay must not execute recorded tasks")
		})
	}
}
