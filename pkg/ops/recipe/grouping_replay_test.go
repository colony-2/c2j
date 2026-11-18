package recipe

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type groupingReplayTask struct {
	calls   int
	retry   bool
	ids     []string
	reports []int
}

func (*groupingReplayTask) Name() string { return "grouped-submit" }
func (w *groupingReplayTask) Run(tc wf.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, nil)
}
func (w *groupingReplayTask) RunTask(ctx context.Context, tc wf.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	w.calls++
	tool := &ops.TaskBasedJobTool{TaskContext: tc, Restricted: permissions.FromContext(ctx)}
	deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).Build()
	child, err := startSingleJob(deps, ctx, SingleRecipeWithRef{SingleRecipe: SingleRecipe{Name: "batch", CellName: "test", Grouping: groupedSpec()}})
	if err != nil {
		return nil, err
	}
	w.ids = append(w.ids, child.JobID)
	w.reports = append(w.reports, len(tool.StartedJobs().Items))
	if w.retry && w.calls == 1 {
		return nil, fmt.Errorf("acceptance before checkpoint")
	}
	return jobdb.NewTaskData(child)
}
func TestGroupedSubmissionCheckpointReplayAndDuplicateWindow(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprint(retry), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := celltest.Context(context.Background())
				raw := toy.New()
				rt := executionruntime.New(raw, execution.Allocation{SchemaVersion: 1})
				celltest.Create(t, rt, "tenant", "test", "example.test/test")
				require.NoError(t, jobdbschema.Register(ctx, raw, "tenant"))
				task := &groupingReplayTask{retry: retry}
				worker := wf.ExecutionWorker{JobType: "parent", Execute: func(c wf.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
					return c.DoTask(wf.TaskCall{Site: "submit", TaskType: task.Name(), ContractVersion: 1, Command: []byte(`{}`), Policy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 2}}}, func(context.Context) (jobdb.TaskData, error) { return jobdb.NewTaskData(nil) })
				}}
				parent, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", Cell: "test", JobType: "parent", Data: jobdb.NewTaskDataOrPanic(nil)}})
				require.NoError(t, err)
				handle, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: parent.JobKey, JobWorker: worker, TaskWorkers: []wf.TaskWorker{rt.WrapTaskWorker(task)}, WorkerID: "parent", LeaseDuration: time.Minute})
				require.NoError(t, err)
				run, err := handle.Run(nil)
				require.NoError(t, err)
				require.Equal(t, wf.JobRunCompleted, run.Status, "%v", run.JobError)
				want := 1
				if retry {
					want = 2
				}
				require.Equal(t, want, task.calls)
				for _, n := range task.reports {
					require.Equal(t, 1, n)
				}
				for _, id := range task.ids {
					require.Equal(t, task.ids[0], id)
				}
				chapters, err := raw.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: task.ids[0]}})
				require.NoError(t, err)
				require.Len(t, chapters, want, "retry may duplicate an accepted contribution")
				jobs, err := raw.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, JobKeys: []jobdb.JobKey{{TenantId: "tenant", JobId: task.ids[0]}}, PageSize: 1})
				require.NoError(t, err)
				require.Len(t, jobs.Jobs, 1)
				require.Empty(t, jobs.Jobs[0].ParentJobID)

				engine, err := wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("tenant").PlusWorkers(worker).BuildEngine()
				require.NoError(t, err)
				_, err = engine.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: parent.JobKey})
				require.NoError(t, err)
				require.Equal(t, want, task.calls, "completed checkpoint must not append during replay")
			})
		})
	}
}
