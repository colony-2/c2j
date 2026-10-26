package recipe

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	workflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type childIdentityTask struct {
	waitForChild bool
	ids          []string
	goals        []string
	tasks        []string
	attempts     []int
}

func (*childIdentityTask) Name() string { return "submit-child" }
func (w *childIdentityTask) Run(tc workflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, input)
}
func (w *childIdentityTask) RunTask(local context.Context, ctx workflow.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	deps := coreops.NewOpDependenciesBuilder().WithJobTool(&coreops.TaskBasedJobTool{TaskContext: ctx, Restricted: permissions.FromContext(local)}).Build()
	input := SingleRecipeWithRef{SingleRecipe: SingleRecipe{Name: "child", CellName: "cell", Git: SingleRecipeGit{BaseRepo: "github.com/example/repo", BaseRef: "main"}}, GitRef: "main"}
	child, err := startSingleJob(deps, local, input)
	if err != nil {
		return nil, err
	}
	repeated, err := startSingleJob(deps, local, input)
	if err != nil {
		return nil, err
	}
	if child.GoalID == "" || repeated.GoalID != child.GoalID || repeated.TenantID != child.TenantID || repeated.JobID != child.JobID {
		return nil, fmt.Errorf("repeated submission changed child identity")
	}
	w.ids = append(w.ids, child.JobID)
	w.goals = append(w.goals, child.GoalID)
	w.tasks = append(w.tasks, ctx.InvocationID)
	w.attempts = append(w.attempts, ctx.AttemptNumber)
	if w.waitForChild {
		if err := ctx.AwaitJobs(child.JobID); err != nil {
			return nil, err
		}
	} else if len(w.ids) == 1 {
		return nil, fmt.Errorf("retry after child submission")
	}
	return jobdb.NewTaskData(child)
}

func TestChildSubmissionIdentityAcrossExecutionRetries(t *testing.T) {
	for _, boundary := range []string{"task", "scope", "job"} {
		t.Run(boundary, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				raw := toy.New()
				rt := executionruntime.New(raw, execution.Allocation{SchemaVersion: 1})
				_, registerErr := raw.RegisterJobSchema(ctx, jobdb.RegisterJobSchemaRequest{TenantId: "test", Schema: jobdbschema.Schema()})
				require.NoError(t, registerErr)
				single := jobdb.RetryPolicy{MaximumAttempts: 1}
				retry := jobdb.RetryPolicy{MaximumAttempts: 2}
				taskPolicy, scopePolicy, jobPolicy := single, single, single
				switch boundary {
				case "task":
					taskPolicy = retry
				case "scope":
					scopePolicy = retry
				case "job":
					jobPolicy = retry
				}
				task := &childIdentityTask{}
				worker := workflow.ExecutionWorker{JobType: "parent", Execute: func(c workflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
					return c.DoScope("block", workflow.ScopeOptions{Retry: scopePolicy}, func(scoped workflow.ExecutionContext) (jobdb.JobData, error) {
						return scoped.DoTask(workflow.TaskCall{Site: "child", TaskType: task.Name(), ContractVersion: 1, Command: []byte(`{}`), Policy: jobdb.RunPolicy{Retry: taskPolicy}}, func(context.Context) (jobdb.TaskData, error) { return jobdb.NewTaskData(nil) })
					})
				}}
				parent, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobType: worker.Name(), Data: jobdb.NewTaskDataOrPanic(nil), RunPolicy: jobdb.RunPolicy{Retry: jobPolicy}}})
				require.NoError(t, err)
				h, err := workflow.GetJobForRun(ctx, rt, workflow.GetJobForRunRequest{JobKey: parent.JobKey, JobWorker: worker, TaskWorkers: []workflow.TaskWorker{rt.WrapTaskWorker(task)}, WorkerID: "test", LeaseDuration: time.Minute})
				require.NoError(t, err)
				out, err := h.Run(nil)
				require.NoError(t, err)
				require.Equal(t, workflow.JobRunCompleted, out.Status, "%v", out.JobError)
				require.Len(t, task.ids, 2)
				require.NotEmpty(t, task.tasks[0])
				if boundary == "task" {
					require.Equal(t, task.ids[0], task.ids[1])
					require.Equal(t, task.goals[0], task.goals[1])
					require.Equal(t, task.tasks[0], task.tasks[1])
					require.Equal(t, []int{1, 2}, task.attempts)
				} else {
					require.NotEqual(t, task.ids[0], task.ids[1])
					require.NotEqual(t, task.goals[0], task.goals[1])
					require.NotEqual(t, task.tasks[0], task.tasks[1])
					require.Equal(t, []int{1, 1}, task.attempts)
				}
				engine, err := workflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("test").PlusWorkers(worker).BuildEngine()
				require.NoError(t, err)
				_, err = engine.ReplayJobRun(ctx, workflow.ReplayRunRequest{JobKey: parent.JobKey})
				require.NoError(t, err)
				require.Len(t, task.ids, 2, "replay must not resubmit children")
			})
		})
	}
}

func TestChildSubmissionIdentityAcrossSuspension(t *testing.T) {
	ctx := context.Background()
	raw := toy.New()
	rt := executionruntime.New(raw, execution.Allocation{SchemaVersion: 1})
	_, err := raw.RegisterJobSchema(ctx, jobdb.RegisterJobSchemaRequest{TenantId: "test", Schema: jobdbschema.Schema()})
	require.NoError(t, err)
	task := &childIdentityTask{waitForChild: true}
	worker := workflow.ExecutionWorker{JobType: "parent", Execute: func(c workflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		return c.DoTask(workflow.TaskCall{Site: "child", TaskType: task.Name(), ContractVersion: 1, Command: []byte(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return jobdb.NewTaskData(nil) })
	}}
	parent, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobType: worker.Name(), Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	run := func() workflow.JobRunOutcome {
		h, err := workflow.GetJobForRun(ctx, rt, workflow.GetJobForRunRequest{JobKey: parent.JobKey, JobWorker: worker, TaskWorkers: []workflow.TaskWorker{rt.WrapTaskWorker(task)}, WorkerID: "test", LeaseDuration: time.Minute})
		require.NoError(t, err)
		out, err := h.Run(nil)
		require.NoError(t, err)
		return out
	}
	require.Equal(t, workflow.JobRunSuspended, run().Status)
	require.Len(t, task.ids, 1)
	require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: jobdb.JobKey{TenantId: "test", JobId: task.ids[0]}}))
	require.Equal(t, workflow.JobRunCompleted, run().Status)
	require.Len(t, task.ids, 2)
	require.Equal(t, task.ids[0], task.ids[1])
	require.Equal(t, task.goals[0], task.goals[1])
	require.Equal(t, task.tasks[0], task.tasks[1])
	require.Equal(t, []int{1, 1}, task.attempts, "resuming unfinished work is the same task attempt")
}
