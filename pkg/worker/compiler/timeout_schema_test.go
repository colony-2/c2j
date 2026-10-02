package compiler

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestC2JSchemaAcceptsTimeoutCompletion(t *testing.T) {
	// Isolate schema ownership: no recipe execution, task replay, deadline
	// scheduling, or chapter collision is involved. JobDB serializes its own
	// typed timeout result; the only changed condition is c2j's job schema.
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, backend)
			timeoutWorker := timeoutReproWorker{"repro", func(jobworkflow.JobContext, jobdb.JobData) (jobdb.JobData, error) {
				return nil, jobdb.NewTimeoutError("job", time.Second, jobdb.TimeoutScopeTotal, nil, false)
			}}
			plain := submitTimeoutRepro(t, rt, "plain-schema", timeoutReproPolicy(time.Minute, time.Second))
			requireTimeoutCompletion(t, rt, plain, runTimeoutRepro(t, rt, plain, timeoutWorker))
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
			require.NoError(t, err)
			submitter := jobdbschema.WorkflowEngine{Engine: engine, Registry: rt.WorkflowRuntime.(jobdb.JobSchemaRegistry)}
			_, gitCtx := GenerateTestContext()
			key, err := starter.StartRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "timeout-repro", RecipeName: "schema-probe", GitRef: gitCtx.ParentRef}, submitter)
			require.NoError(t, err)
			timeoutWorker.name = "recipe"
			runnable, err := jobworkflow.GetJobForRun(context.Background(), rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: timeoutWorker, WorkerID: "schema-probe", LeaseDuration: time.Second})
			require.NoError(t, err)
			out, err := runnable.Run(nil)
			require.NoError(t, err)
			requireTimeoutCompletion(t, rt, key, out)
			require.Len(t, rt.completions, 2)
		})
	}
}
