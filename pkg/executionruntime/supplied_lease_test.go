package executionruntime

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestSuppliedLeaseUsesExecutionAdmission(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "remote", "16Gi", nil)
	// Publish the recipe's requirements through an ordinary reschedule.
	out, _ := f.run(t, "16Gi", nil)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	info, err := f.runtime.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{f.key.TenantId}, JobKeys: []jobdb.JobKey{f.key}})
	require.NoError(t, err)
	require.Len(t, info.Jobs, 1)
	require.NotNil(t, info.Jobs[0].NextRoute)
	claimed, err := f.runtime.GetJobLease(ctx, jobdb.GetJobLeaseRequest{
		JobKey: f.key, WorkerID: "dispatcher", LeaseDuration: time.Minute,
		Routes: []jobdb.Route{*info.Jobs[0].NextRoute},
	})
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NoError(t, claimed.KeepAlive(ctx))
	var event *compiler.ExecutionHandoff
	runtime := New(f.runtime, allocation("2Gi"), func(e compiler.ExecutionHandoff) { event = &e })
	outcome, err := runFixtureLease(t, f, runtime, claimed, f.tasks)
	require.NoError(t, err)
	require.Equal(t, jobworkflow.JobRunSuspended, outcome.Status)
	require.NotNil(t, event)
	require.Zero(t, f.first.Load())
	require.Zero(t, f.second.Load())
	// The supplied lease was surrendered through the normal handoff path.
	require.Error(t, claimed.KeepAlive(ctx))
}

// Fail immediately if any supplied invocation reaches an acquisition API.
type noClaimsRuntime struct {
	jobdb.WorkflowRuntime
	t *testing.T
}

func (r noClaimsRuntime) GetJobLease(context.Context, jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	r.t.Error("unexpected lease acquisition")
	return nil, jobdb.ErrExecutionLeaseLost
}
func (r noClaimsRuntime) PollWork(context.Context, jobdb.PollWorkRequest) ([]jobdb.ExecutionLease, error) {
	r.t.Error("unexpected work polling")
	return nil, jobdb.ErrExecutionLeaseLost
}
func runFixtureLease(t *testing.T, f *fixture, rt *Runtime, supplied jobdb.ExecutionLease, tasks []jobworkflow.TaskWorker) (jobworkflow.JobRunOutcome, error) {
	t.Helper()
	rt.WorkflowRuntime = noClaimsRuntime{f.runtime, t}
	worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{Allocation: rt.Allocation, StageExecution: rt.Stage, WrapTaskWorker: rt.WrapTaskWorker, OnExecutionHandoff: rt.OnHandoff})
	wrapped := make([]jobworkflow.TaskWorker, len(tasks))
	for i, task := range tasks {
		wrapped[i] = rt.WrapTaskWorker(task)
	}
	return rt.RunWithLease(context.Background(), supplied, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: worker, TaskWorkers: wrapped}, nil)
}
func TestRunSuppliedLeaseReplaysTaskRouteAndPreservesState(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newFixture(t, backend, "2Gi", nil)
			claim := func(route jobdb.Route) jobdb.ExecutionLease {
				l, err := f.runtime.GetJobLease(context.Background(), jobdb.GetJobLeaseRequest{JobKey: f.key, WorkerID: "dispatcher", Routes: []jobdb.Route{route}, LeaseDuration: time.Minute})
				require.NoError(t, err)
				require.NotNil(t, l)
				return l
			}
			rt := New(f.runtime, allocation("2Gi"), nil)
			// Execute the first task, then let normal dispatch reschedule the missing second task.
			out, err := runFixtureLease(t, f, rt, claim(jobdb.Route{JobType: "recipe"}), f.tasks[:1])
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.EqualValues(t, 1, f.first.Load())
			require.Zero(t, f.second.Load())
			require.Empty(t, rt.leases)
			require.NotNil(t, out.NextRoute)
			require.Equal(t, f.tasks[1].Name(), out.NextRoute.TaskType)
			rt = New(f.runtime, allocation("2Gi"), nil)
			out, err = runFixtureLease(t, f, rt, claim(*out.NextRoute), f.tasks)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			require.EqualValues(t, 1, f.first.Load())
			require.EqualValues(t, 1, f.second.Load())
			require.Empty(t, rt.leases)
		})
	}
}

type delayedSuppliedTask struct{ jobworkflow.TaskWorker }

func (w delayedSuppliedTask) Run(ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	// Exceed the original SQLite lease lifetime so lost wrapper state/renewal
	// support fails through actual scheduler mutations, not just mock assertions.
	time.Sleep(250 * time.Millisecond)
	return w.TaskWorker.Run(ctx, input)
}
func TestRenewedSuppliedLeaseRetainsStagedDemand(t *testing.T) {
	f := newFixture(t, "sqlite", "2Gi", nil)
	supplied, err := f.runtime.GetJobLease(context.Background(), jobdb.GetJobLeaseRequest{JobKey: f.key, WorkerID: "dispatcher", Routes: []jobdb.Route{{JobType: "recipe"}}, LeaseDuration: 100 * time.Millisecond})
	require.NoError(t, err)
	require.NotNil(t, supplied)
	rt := New(f.runtime, allocation("2Gi"), nil)
	out, err := runFixtureLease(t, f, rt, supplied, []jobworkflow.TaskWorker{delayedSuppliedTask{f.tasks[0]}})
	require.NoError(t, err)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	info, err := f.runtime.GetJob(context.Background(), f.key)
	require.NoError(t, err)
	require.Contains(t, string(info.ClientPayload), `"memory":"2Gi"`)
	require.Empty(t, rt.leases)
}
