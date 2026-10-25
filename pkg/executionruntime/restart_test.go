package executionruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

const restartRequirementsRecipe = `id: rewind
execution: {resources: {memory: 2Gi}}
sequence:
 - id: increase
   op: needs_a
 - id: decrease
   op: needs_b
 - id: finish
   op: needs_c
`

func TestRestartReconstructsRequirementsAtSelectedScope(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, scope := range []string{"increase", "decrease", "finish"} {
			t.Run(backend+"/"+scope, func(t *testing.T) {
				ctx := context.Background()
				f, tasks := newNodeFixture(t, backend, restartRequirementsRecipe)
				r, err := recipe.LoadRecipeFromString([]byte(restartRequirementsRecipe))
				require.NoError(t, err)
				submitNodeRecipe(t, f, *r, starter.StartRecipeJobOptions{ExecutionRequirements: execution.Requirements{Resources: execution.Resources{Memory: str("4Gi")}}})
				tasks[0].patch = &execution.Requirements{Resources: execution.Resources{Memory: str("16Gi")}}
				tasks[1].patch = &execution.Requirements{Resources: execution.Resources{Memory: str("8Gi")}}
				out, event := f.run(t, "4Gi", f.tasks)
				require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
				require.Equal(t, "16Gi", *event.Demand.Effective.Resources.Memory)
				out, event = f.run(t, "16Gi", f.tasks[:2])
				require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
				require.Nil(t, event)
				out, event = f.run(t, "8Gi", f.tasks)
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
				require.Nil(t, event)
				old, err := f.runtime.GetJob(ctx, f.key)
				require.NoError(t, err)
				latest, err := execution.PayloadDemand(old.ClientPayload)
				require.NoError(t, err)
				require.Equal(t, "8Gi", *latest.Effective.Resources.Memory)
				run, err := jobdb.GetJobRun(ctx, f.runtime, jobdb.GetJobRunRequest{JobKey: f.key})
				require.NoError(t, err)
				var scopeID string
				for _, entry := range run.Execution {
					if entry.Record.Kind == "scope" && strings.Contains(entry.Record.ID, `scope:"op:`+scope+`"`) {
						scopeID = entry.Record.ID
					}
				}
				require.NotEmpty(t, scopeID)
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(f.runtime).BuildEngine()
				require.NoError(t, err)
				submitter := jobdbschema.WorkflowEngine{Engine: engine, Registry: f.runtime.(jobdb.JobSchemaRegistry)}
				prior := f.key
				opts := starter.RestartOptions{Goal: "new", RequestID: "restart-requirements"}
				ref, err := starter.RestartRecipeJob(ctx, submitter, prior, scopeID, nil, opts)
				require.NoError(t, err)
				f.key = ref.Key()
				info, err := f.runtime.GetJob(ctx, f.key)
				require.NoError(t, err)
				initial, err := execution.PayloadDemand(info.ClientPayload)
				require.NoError(t, err)
				require.NotNil(t, initial)
				require.Equal(t, "4Gi", *initial.Effective.Resources.Memory, "restart must use original overrides, not the latest payload")
				out, event = f.run(t, "4Gi", f.tasks)
				require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
				require.NotNil(t, event)
				wantMemory := "16Gi"
				if scope == "finish" {
					wantMemory = "8Gi"
				}
				require.Equal(t, wantMemory, *event.Demand.Effective.Resources.Memory)
				if scope == "increase" {
					require.EqualValues(t, 2, tasks[0].runs.Load())
				} else {
					require.EqualValues(t, 1, tasks[0].runs.Load())
				}
				require.EqualValues(t, 1, tasks[1].runs.Load())
				require.EqualValues(t, 1, tasks[2].runs.Load())
				out, event = f.run(t, "32Gi", f.tasks)
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
				require.Nil(t, event)
				if scope == "finish" {
					require.EqualValues(t, 1, tasks[1].runs.Load())
				} else {
					require.EqualValues(t, 2, tasks[1].runs.Load())
				}
				require.EqualValues(t, 2, tasks[2].runs.Load())
				again, err := starter.RestartRecipeJob(ctx, submitter, prior, scopeID, nil, opts)
				require.NoError(t, err)
				require.Equal(t, ref, again, "idempotent restart must keep its original initialization")

				// JobDB must preserve submission metadata across every restart,
				// so restarting this candidate also starts from the original hint.
				restartedRun, err := jobdb.GetJobRun(ctx, f.runtime, jobdb.GetJobRunRequest{JobKey: f.key})
				require.NoError(t, err)
				require.JSONEq(t, string(run.Job.Metadata), string(restartedRun.Job.Metadata))
				next, err := starter.RestartRecipeJob(ctx, submitter, f.key, scopeID, nil, starter.RestartOptions{Goal: "new", RequestID: "restart-again"})
				require.NoError(t, err)
				f.key = next.Key()
				nextInfo, err := f.runtime.GetJob(ctx, f.key)
				require.NoError(t, err)
				nextInitial, err := execution.PayloadDemand(nextInfo.ClientPayload)
				require.NoError(t, err)
				require.Equal(t, initial, nextInitial, "repeated restart must retain original requirements")
				out, event = f.run(t, "32Gi", f.tasks)
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
				require.Nil(t, event)

			})
		}
	}
}
