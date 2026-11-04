package executionruntime

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestSuppliedLeaseUsesExecutionAdmission(t *testing.T) {
	ctx := celltest.Context(context.Background())
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
	runtime := New(f.runtime, allocation("2Gi"))
	outcome, err := runFixtureLease(t, f, runtime, claimed, f.tasks)
	require.NoError(t, err)
	require.Equal(t, jobworkflow.JobRunSuspended, outcome.Status)
	event, err := compiler.ExecutionHandoffFromOutcome(outcome)
	require.NoError(t, err)
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
	worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{Allocation: rt.Allocation, StageExecution: rt.Stage, WrapTaskWorker: rt.WrapTaskWorker})
	wrapped := make([]jobworkflow.TaskWorker, len(tasks))
	for i, task := range tasks {
		wrapped[i] = rt.WrapTaskWorker(task)
	}
	return rt.RunWithLease(celltest.Context(context.Background()), supplied, jobworkflow.GetJobForRunRequest{JobKey: f.key, JobWorker: worker, TaskWorkers: append(wrapped, compiler.NewCellContextTaskWorker())}, nil)
}
func TestRunSuppliedLeaseReplaysTaskRouteAndPreservesState(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newFixture(t, backend, "2Gi", nil)
			claim := func(route jobdb.Route) jobdb.ExecutionLease {
				l, err := f.runtime.GetJobLease(celltest.Context(context.Background()), jobdb.GetJobLeaseRequest{JobKey: f.key, WorkerID: "dispatcher", Routes: []jobdb.Route{route}, LeaseDuration: time.Minute})
				require.NoError(t, err)
				require.NotNil(t, l)
				return l
			}
			rt := New(f.runtime, allocation("2Gi"))
			// Execute the first task, then let normal dispatch reschedule the missing second task.
			out, err := runFixtureLease(t, f, rt, claim(jobdb.Route{JobType: "recipe"}), f.tasks[:1])
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.EqualValues(t, 1, f.first.Load())
			require.Zero(t, f.second.Load())
			require.Empty(t, rt.leases)
			require.NotNil(t, out.NextRoute)
			require.Equal(t, f.tasks[1].Name(), out.NextRoute.TaskType)
			rt = New(f.runtime, allocation("2Gi"))
			out, err = runFixtureLease(t, f, rt, claim(*out.NextRoute), f.tasks)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			require.EqualValues(t, 1, f.first.Load())
			require.EqualValues(t, 1, f.second.Load())
			require.Empty(t, rt.leases)
		})
	}
}

type observedRenewal struct {
	started chan struct{}
	renewed chan struct{}
	once    sync.Once
}

type observedSuppliedLease struct {
	jobdb.RenewableExecutionLease
	observation *observedRenewal
}

func (l observedSuppliedLease) LeaseSchemaHash() string {
	return l.RenewableExecutionLease.(interface{ LeaseSchemaHash() string }).LeaseSchemaHash()
}

func (l observedSuppliedLease) Renew(ctx context.Context) (jobdb.RenewableExecutionLease, error) {
	next, err := l.RenewableExecutionLease.Renew(ctx)
	if err != nil {
		return nil, err
	}
	select {
	case <-l.observation.started:
		l.observation.once.Do(func() { close(l.observation.renewed) })
	default:
	}
	return observedSuppliedLease{next, l.observation}, nil
}

type renewalWaitingTask struct {
	jobworkflow.TaskWorker
	observation *observedRenewal
}

func (w renewalWaitingTask) Run(ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(ctx.Context(), ctx, input)
}
func (w renewalWaitingTask) RunTask(local context.Context, ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	// Demand is staged before the task runs. Wait for a real SQLite renewal
	// after that point, so the subsequent reschedule uses a refreshed snapshot.
	close(w.observation.started)
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-w.observation.renewed:
	case <-local.Done():
		return nil, local.Err()
	case <-timer.C:
		return nil, fmt.Errorf("no lease renewal observed after task started")
	}
	// Yield before returning a task result: a later node must not get a chance
	// to stage demand again and hide state lost during renewal.
	return nil, ctx.Yield(local, jobworkflow.YieldRequest{
		Reschedule: jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}},
	})
}
func TestRenewedSuppliedLeaseRetainsStagedDemand(t *testing.T) {
	f := newFixture(t, "sqlite", "2Gi", nil)
	// Allow CI scheduling and SQLite I/O delays without making expiry the test's clock.
	supplied, err := f.runtime.GetJobLease(celltest.Context(context.Background()), jobdb.GetJobLeaseRequest{JobKey: f.key, WorkerID: "dispatcher", Routes: []jobdb.Route{{JobType: "recipe"}}, LeaseDuration: 10 * time.Second})
	require.NoError(t, err)
	require.NotNil(t, supplied)
	observation := &observedRenewal{started: make(chan struct{}), renewed: make(chan struct{})}
	observed := observedSuppliedLease{supplied.(jobdb.RenewableExecutionLease), observation}
	rt := New(f.runtime, allocation("2Gi"))
	out, err := runFixtureLease(t, f, rt, observed, []jobworkflow.TaskWorker{renewalWaitingTask{f.tasks[0], observation}})
	require.NoError(t, err)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	info, err := f.runtime.GetJob(celltest.Context(context.Background()), f.key)
	require.NoError(t, err)
	require.Contains(t, string(info.ClientPayload), `"memory":"2Gi"`)
	require.Empty(t, rt.leases)
}
