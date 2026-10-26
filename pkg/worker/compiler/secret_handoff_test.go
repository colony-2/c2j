package compiler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Hold the response after the scheduler has accepted a dependency wait. Another
// worker can acquire the job before the old execution learns it must suspend.
type delayedHandoffRuntime struct {
	jobdb.WorkflowRuntime
	accepted chan struct{}
	release  chan struct{}
	once     sync.Once
}

type delayedHandoffLease struct {
	jobdb.ExecutionLease
	runtime *delayedHandoffRuntime
}

func (r *delayedHandoffRuntime) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	l, err := r.WorkflowRuntime.GetJobLease(ctx, req)
	if err != nil || l == nil {
		return l, err
	}
	return &delayedHandoffLease{l, r}, nil
}

func (l *delayedHandoffLease) Reschedule(ctx context.Context, req jobdb.RescheduleExecutionRequest) error {
	if err := l.ExecutionLease.Reschedule(ctx, req); err != nil {
		return err
	}
	l.runtime.once.Do(func() {
		close(l.runtime.accepted)
		<-l.runtime.release
	})
	return nil
}

type goalHandoffExecutor struct {
	DefaultRecipeExecutor
	goal   string
	inTask bool
}

func (e goalHandoffExecutor) ExecuteRecipe(ctx workflow.Context, _ recipe.Recipe, _ map[string]interface{}, _ contextual.JobContext, _ contextual.GitCommitContext, _ ...ExecutionOptions) (map[string]interface{}, []jobdb.Artifact, error) {
	if e.inTask {
		_, err := workflow.ValueCall(ctx.JobContext, "wait", "handoff-wait", jobdb.RunPolicy{}, e.goal)
		return map[string]interface{}{"done": true}, nil, err
	}
	_, err := ctx.JobContext.AwaitGoals(e.goal)
	return map[string]interface{}{"done": true}, nil, err
}

type goalHandoffTask struct{ goal string }

func (goalHandoffTask) Name() string { return "handoff-wait" }
func (w goalHandoffTask) Run(ctx jobworkflow.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	_, err := ctx.AwaitGoals(w.goal)
	return jobdb.NewTaskDataOrPanic(true), err
}

func TestRecipeReplayDoesNotWaitForPreviousActivationCleanup(t *testing.T) {
	for _, mode := range []string{"workflow wait", "task wait"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				base := toy.New()
				child, err := base.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{
					TenantId: "handoff", JobType: "child", Data: jobdb.NewTaskDataOrPanic(nil),
				}})
				require.NoError(t, err)
				childWorker := jobworkflow.ExecutionWorker{JobType: "child", Execute: func(jobworkflow.ExecutionContext, jobdb.JobData) (jobdb.JobData, error) {
					return jobdb.NewTaskDataOrPanic(nil), nil
				}}
				childRun, err := jobworkflow.GetJobForRun(ctx, base, jobworkflow.GetJobForRunRequest{
					JobKey: child.JobKey, JobWorker: childWorker, WorkerID: "child", LeaseDuration: time.Hour,
				})
				require.NoError(t, err)
				childOut, err := childRun.Run(nil)
				require.NoError(t, err)
				require.Equal(t, jobworkflow.JobRunCompleted, childOut.Status)

				rt := &delayedHandoffRuntime{WorkflowRuntime: base, accepted: make(chan struct{}), release: make(chan struct{})}
				defer close(rt.release)
				r, err := recipe.LoadRecipeFromString([]byte("id: parent\nsequence: []\n"))
				require.NoError(t, err)
				var resolutions atomic.Int32
				resolver := NewRecipeSourceResolver(RecipeSourceResolverOptions{
					RecipeRefResolver: NewProviderBackedRecipeRefResolver(func(string, string) (*recipe.Recipe, error) {
						resolutions.Add(1)
						return r, nil
					}),
				})
				worker := NewRecipeJobWorker(RecipeJobWorkerOptions{
					RootSourceResolver: resolver, Executor: goalHandoffExecutor{goal: child.GoalID, inTask: mode == "task wait"},
				})
				// Keep the source unembedded so replay must consume root-source at ordinal 3.
				parent, err := base.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{
					TenantId: "handoff", JobType: worker.Name(), Data: jobdb.NewTaskDataOrPanic(workflowctl.StartJob{RecipeName: "parent", TenantId: "handoff"}),
				}})
				require.NoError(t, err)
				acquire := func(id string) *jobworkflow.JobRunnable {
					t.Helper()
					h, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{
						JobKey: parent.JobKey, JobWorker: worker, WorkerID: id, LeaseDuration: time.Hour,
						TaskWorkers: []jobworkflow.TaskWorker{newRootSourceResolutionTaskWorker(resolver), goalHandoffTask{goal: child.GoalID}},
					})
					require.NoError(t, err)
					return h
				}
				first := acquire("first")
				type result struct {
					out jobworkflow.JobRunOutcome
					err error
				}
				firstDone, nextDone := make(chan result, 1), make(chan result, 1)
				go func() { out, err := first.Run(nil); firstDone <- result{out, err} }()
				<-rt.accepted
				resolvedBeforeResume := resolutions.Load()
				require.Positive(t, resolvedBeforeResume)
				next := acquire("next")
				go func() { out, err := next.Run(nil); nextDone <- result{out, err} }()
				// The next activation must complete while the old reschedule
				// response remains blocked. No consumer lifecycle lock.
				got := <-nextDone
				require.NoError(t, got.err)
				require.NoError(t, got.out.JobError)
				require.Equal(t, jobworkflow.JobRunCompleted, got.out.Status)
				data, err := got.out.Output.GetData()
				require.NoError(t, err)
				require.JSONEq(t, `{"done":true}`, string(data))
				rt.release <- struct{}{}
				prior := <-firstDone
				require.NoError(t, prior.err)
				require.NoError(t, prior.out.JobError)
				// Run classifies dependency handoffs from current scheduler state;
				// the new activation may already have completed by that read.
				require.Contains(t, []jobworkflow.JobRunStatus{jobworkflow.JobRunSuspended, jobworkflow.JobRunCompleted}, prior.out.Status)
				require.Equal(t, resolvedBeforeResume, resolutions.Load(), "root-source must replay its cached result")
			})
		})
	}
}
