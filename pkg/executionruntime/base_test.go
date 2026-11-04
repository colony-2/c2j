package executionruntime

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func baseAllocation(base string) execution.Allocation {
	return execution.Allocation{SchemaVersion: execution.SchemaVersion, Base: base}
}

func TestBaseScopeHandoffAndSuppliedLeaseReplay(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f, tasks := newNodeFixture(t, backend, `id: bases
execution_needs: {base: 'ubuntu*'}
sequence:
 - id: linux
   op: needs_a
 - id: mac
   execution_needs: {base: 'macos*'}
   op: needs_b
 - id: linux_again
   op: needs_c
`)
			ctx := celltest.Context(context.Background())
			out, event := runNode(t, f, baseAllocation("ubuntu-26-arm64"), f.tasks)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.NotNil(t, event)
			require.Equal(t, "macos*", *event.Demand.Effective.Base)
			require.Equal(t, "base", event.Mismatches[0].Field)
			require.EqualValues(t, 1, tasks[0].runs.Load())
			require.Zero(t, tasks[1].runs.Load())
			// Claim the published demand, then run precisely that supplied lease.
			supplied, err := f.runtime.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: f.key, WorkerID: "mac-provider", Routes: []jobdb.Route{*out.NextRoute}, LeaseDuration: time.Minute})
			require.NoError(t, err)
			require.NotNil(t, supplied)
			demand, err := execution.PayloadDemand(supplied.ClientPayload())
			require.NoError(t, err)
			require.Equal(t, "macos*", *demand.Effective.Base)
			rt := New(f.runtime, baseAllocation("macos-123-456"))
			out, err = runFixtureLease(t, f, rt, supplied, f.tasks)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			event, err = compiler.ExecutionHandoffFromOutcome(out)
			require.NoError(t, err)
			require.Equal(t, "ubuntu*", *event.Demand.Effective.Base)
			require.EqualValues(t, 1, tasks[1].runs.Load())
			require.Zero(t, tasks[2].runs.Load())
			require.Error(t, supplied.KeepAlive(ctx), "handoff ends the supplied lease")
			out, event = runNode(t, f, baseAllocation("ubuntu-26-arm64"), f.tasks)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			require.Nil(t, event)
			for _, task := range tasks {
				require.EqualValues(t, 1, task.runs.Load())
			}
			// Completed replay must not ask for either historical environment.
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(f.runtime).WithWorkerTenantId("tenant").PlusWorkers(compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
			require.NoError(t, err)
			_, err = engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: f.key, JobWorker: compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err)
		})
	}
}

func TestBaseWildcardWithMissingAllocationAndDurableChange(t *testing.T) {
	f, tasks := newNodeFixture(t, "sqlite", `id: wildcard
execution: {base: '*'}
sequence:
 - id: change
   op: needs_a
 - id: next
   op: needs_b
`)
	tasks[0].patch = &execution.Requirements{Base: str("macos*")}
	out, event := runNode(t, f, baseAllocation(""), f.tasks)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	require.EqualValues(t, 1, tasks[0].runs.Load())
	require.Zero(t, tasks[1].runs.Load())
	require.Equal(t, "macos*", *event.Demand.Effective.Base)
	require.Equal(t, "unknown_allocation", event.Mismatches[0].Reason)
	out, event = runNode(t, f, baseAllocation("macos-123-456"), f.tasks)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.EqualValues(t, 1, tasks[0].runs.Load(), "cached directive changes requirements without rerunning the task")
	require.EqualValues(t, 1, tasks[1].runs.Load())
}
