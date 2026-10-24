package recipe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type goalWaitTask struct {
	ctl   *workerworkflow.SWFWorkflowControl
	input StartedJob
	calls atomic.Int32
	soft  bool
}

func (*goalWaitTask) Name() string { return "goal-wait" }
func (t *goalWaitTask) Run(c wf.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	t.calls.Add(1)
	d := ops.NewOpDependenciesBuilder().WithWorkflowControl(t.ctl).WithJobTool(&ops.TaskBasedJobTool{TaskContext: c}).Build()
	if t.soft {
		out, err := awaitResultSoft(d, c.Context(), AwaitResultSoftInput{Target: WaitTarget{Goal: t.input.GoalID}})
		if err != nil {
			return nil, err
		}
		return jobdb.NewTaskData(out)
	}
	out, err := waitAndGetRecipeOutput(d, c.Context(), t.input)
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(out)
}

func TestGoalWinnerCollectionRecoveryAndExplicitFailure(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%v", backend, fail), func(t *testing.T) {
				ctx := context.Background()
				db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
				var rt jobdb.WorkflowRuntime = db
				if backend == "remote" {
					s := httptest.NewServer(remote.NewServer(db))
					t.Cleanup(s.Close)
					rt, err = remote.New(s.URL, s.Client())
					require.NoError(t, err)
				}
				producer := wf.ExecutionWorker{JobType: "producer", Execute: func(c wf.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
					return jobdb.NewTaskDataOrPanic(map[string]any{"output": map[string]any{"winner": c.GetJobKey().JobId}, "git": map[string]any{"hash": "winner-hash"}}, jobdb.NewArtifactFromBytes("result.txt", []byte(c.GetJobKey().JobId))), nil
				}}
				submit := func(w wf.ExecutionWorker) jobdb.JobHandle {
					h, e := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: w.Name(), Data: jobdb.NewTaskDataOrPanic(nil)}})
					require.NoError(t, e)
					return h
				}
				original := submit(producer)
				engine, err := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
				require.NoError(t, err)
				ctl := &workerworkflow.SWFWorkflowControl{Engine: jobdbschema.WorkflowEngine{Engine: engine, Registry: rt.(jobdb.JobSchemaRegistry)}}
				task := &goalWaitTask{ctl: ctl, input: StartedJob{JobID: original.JobKey.JobId, GoalID: original.GoalID}, soft: fail}
				consumer := wf.ExecutionWorker{JobType: "consumer", Execute: func(c wf.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
					return c.DoTask(wf.TaskCall{Site: "wait", TaskType: task.Name(), ContractVersion: 1, Command: []byte(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return in, nil })
				}}
				parent := submit(consumer)
				run := func(h jobdb.JobHandle, w wf.ExecutionWorker, tasks ...wf.TaskWorker) wf.JobRunOutcome {
					r, e := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: w, TaskWorkers: tasks, WorkerID: "test", LeaseDuration: time.Minute})
					require.NoError(t, e)
					o, e := r.Run(nil)
					require.NoError(t, e)
					return o
				}
				require.Equal(t, wf.JobRunSuspended, run(parent, consumer, task).Status)
				require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: original.JobKey}))
				key := jobdb.GoalKey{TenantId: "tenant", GoalID: original.GoalID}
				goal, err := rt.GetGoal(ctx, key)
				require.NoError(t, err)
				require.Equal(t, jobdb.GoalOpen, goal.State)
				var winner jobdb.JobHandle
				if fail {
					goal, err = rt.FailGoal(ctx, jobdb.FailGoalRequest{GoalKey: key, Message: "no further attempts"})
					require.NoError(t, err)
				} else {
					winner = submit(producer)
					_, err = rt.AddGoalCandidate(ctx, jobdb.AddGoalCandidateRequest{GoalKey: key, JobID: winner.JobKey.JobId})
					require.NoError(t, err)
					require.Equal(t, wf.JobRunCompleted, run(winner, producer).Status)
				}
				out := run(parent, consumer, task)
				require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
				if fail {
					var got ChildStatusOutput
					require.NoError(t, json.Unmarshal(out.Output.GetDataOrPanic(), &got))
					require.Equal(t, goal.Failure, got.GoalFailure)
					require.Equal(t, jobdb.GoalFailed, got.GoalState)
				} else {
					var got SingleRecipeOutput
					require.NoError(t, json.Unmarshal(out.Output.GetDataOrPanic(), &got))
					require.Equal(t, ResultSource{Goal: original.GoalID, Job: winner.JobKey.JobId}, got.Result)
					require.Equal(t, winner.JobKey.JobId, got.Outputs["winner"])
					require.Contains(t, got.Artifacts, "result.txt")
				}
				count := task.calls.Load()
				replay, err := wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("tenant").PlusWorkers(consumer, task).BuildEngine()
				require.NoError(t, err)
				_, err = replay.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: parent.JobKey})
				require.NoError(t, err)
				require.Equal(t, count, task.calls.Load(), "replay must use recorded winner and result")
			})
		}
	}
}
