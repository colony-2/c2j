package recipe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/starter"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type groupedWaitTask struct {
	ctl        *workerworkflow.SWFWorkflowControl
	input      StartedJob
	completion bool
}

func (*groupedWaitTask) Name() string { return "grouped-wait" }
func (w *groupedWaitTask) Run(tc wf.TaskContext, saved jobdb.TaskData) (jobdb.TaskData, error) {
	if saved != nil {
		raw, err := saved.GetData()
		if err != nil {
			return nil, err
		}
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &w.input); err != nil {
				return nil, err
			}
		}
	}
	d := ops.NewOpDependenciesBuilder().WithWorkflowControl(w.ctl).WithJobTool(&ops.TaskBasedJobTool{TaskContext: tc}).Build()
	if w.completion {
		out, err := waitAndNoResult(d, tc.Context(), StartedJobs{Items: []StartedJob{w.input}})
		if err != nil {
			return nil, err
		}
		return jobdb.NewTaskData(out)
	}
	out, err := waitAndGetRecipeOutput(d, tc.Context(), w.input)
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(out)
}

func TestGroupedWaitsRepairCancellationAndSharedResults(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		for _, outcome := range []string{"success", "failed", "cancelled"} {
			t.Run(backend+"/"+outcome, func(t *testing.T) {
				ctx := jobdb.WithPrimaryGoalSelectionAuthorizer(celltest.Context(context.Background()), func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error { return nil })
				db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
				var rt jobdb.ConsumerRuntime = db
				if backend == "http" {
					handler := remote.NewConsumerServer(db)
					s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						handler.ServeHTTP(w, r.WithContext(jobdb.WithPrimaryGoalSelectionAuthorizer(celltest.Context(r.Context()), func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error { return nil })))
					}))
					t.Cleanup(s.Close)
					rt, err = remote.New(s.URL, s.Client())
					require.NoError(t, err)
				}
				celltest.Create(t, rt, "tenant", "test", "example.test/test")
				engine, err := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
				require.NoError(t, err)
				eng := jobdbschema.WorkflowEngine{Engine: engine, Registry: rt.(jobdb.JobSchemaRegistry)}
				ctl := &workerworkflow.SWFWorkflowControl{Engine: eng}
				start := workflowctl.StartJob{TenantId: "tenant", Cell: "test", RecipeName: "batch"}
				a, err := starter.StartOrAppendRecipeJob(ctx, start, groupedSpec(), eng)
				require.NoError(t, err)
				b, err := starter.StartOrAppendRecipeJob(ctx, start, groupedSpec(), eng)
				require.NoError(t, err)
				require.Equal(t, a, b)
				policy, err := (WaitPolicy{}).forSubmission(true, true)
				require.NoError(t, err)
				input := StartedJob{TenantID: a.TenantID, JobID: a.JobID, GoalID: a.GoalID, Wait: policy}
				consumer := wf.ExecutionWorker{JobType: "consumer", Execute: func(c wf.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
					return c.DoTask(wf.TaskCall{Site: "wait", TaskType: "grouped-wait", ContractVersion: 1, Command: []byte(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return jobdb.NewTaskData(nil) })
				}}
				parents := []jobdb.JobHandle{}
				tasks := []*groupedWaitTask{}
				for i := range 6 {
					h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", Cell: "test", JobType: "consumer", Data: jobdb.NewTaskDataOrPanic(nil)}})
					require.NoError(t, err)
					w := &groupedWaitTask{ctl: ctl, input: input}
					if i == 2 {
						w.completion = true
						w.input.Wait, err = (WaitPolicy{}).forSubmission(true, false)
						require.NoError(t, err)
					}
					if i == 3 {
						w.input.Wait = WaitPolicy{On: "goal", Condition: "success"}
					}
					if i == 5 {
						// The saved pre-grouping shape has no wait/grouping fields.
						w.input = StartedJob{}
					}
					parents, tasks = append(parents, h), append(tasks, w)
				}
				legacyJSON, err := json.Marshal(map[string]any{"job_id": a.JobID, "goal_id": a.GoalID})
				require.NoError(t, err)
				legacyConsumer := wf.ExecutionWorker{JobType: "consumer", Execute: func(c wf.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
					return c.DoTask(wf.TaskCall{Site: "wait", TaskType: "grouped-wait", ContractVersion: 1, Command: []byte(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return jobdb.NewTaskData(json.RawMessage(legacyJSON)) })
				}}
				run := func(key jobdb.JobKey, worker wf.JobWorker, tasks ...wf.TaskWorker) wf.JobRunOutcome {
					h, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: key, JobWorker: worker, TaskWorkers: tasks, WorkerID: "test", LeaseDuration: time.Minute})
					require.NoError(t, err)
					o, err := h.Run(nil)
					require.NoError(t, err)
					return o
				}
				for i, parent := range parents {
					worker := consumer
					if i == 5 {
						worker = legacyConsumer
					}
					require.Equal(t, wf.JobRunSuspended, run(parent.JobKey, worker, tasks[i]).Status)
				}
				// Cancelling one suspended caller must not cancel/withdraw either
				// accepted contribution or prevent the remaining callers waiting.
				require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: parents[0].JobKey}))
				info, err := rt.GetJob(ctx, a.Key())
				require.NoError(t, err)
				require.Equal(t, jobdb.JobStatusReady, info.Status)
				chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: a.Key()})
				require.NoError(t, err)
				require.Len(t, chapters, 2)
				producer := wf.ExecutionWorker{JobType: "recipe", Execute: func(c wf.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
					return jobdb.NewTaskData(map[string]any{"output": map[string]any{"batch": "shared", "producer": c.GetJobKey().JobId}}, jobdb.NewArtifactFromBytes("batch.txt", []byte("actual shared bytes")))
				}}
				if outcome == "cancelled" {
					require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: a.Key(), Reason: "operator cancelled batch"}))
				} else if outcome == "failed" {
					failed := wf.ExecutionWorker{JobType: "recipe", Execute: func(wf.ExecutionContext, jobdb.JobData) (jobdb.JobData, error) {
						return nil, jobdb.AppError{Payload: jobdb.AppErrorPayload{Message: "batch failed"}}
					}}
					require.Equal(t, wf.JobRunFailed, run(a.Key(), failed).Status)
				} else {
					require.Equal(t, wf.JobRunCompleted, run(a.Key(), producer).Status)
				}
				var exact []SingleRecipeOutput
				for _, i := range []int{1, 4} {
					out := run(parents[i].JobKey, consumer, tasks[i])
					if outcome != "success" {
						require.Equal(t, wf.JobRunFailed, out.Status)
						continue
					}
					require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
					var got SingleRecipeOutput
					require.NoError(t, json.Unmarshal(out.Output.GetDataOrPanic(), &got))
					require.Equal(t, ResultSource{Job: a.JobID}, got.Result)
					require.Equal(t, "shared", got.Outputs["batch"])
					ref := got.Artifacts["batch.txt"]
					require.NotNil(t, ref.Stored)
					require.Equal(t, a.JobID, ref.Stored.Key.JobId)
					reader, err := rt.OpenArtifact(ctx, jobdb.ArtifactRef{JobKey: a.Key(), Ordinal: ref.Stored.Key.TaskOrdinal, Name: ref.Stored.Key.Name})
					require.NoError(t, err)
					stream, err := reader.Open()
					require.NoError(t, err)
					bytes, err := io.ReadAll(stream)
					require.NoError(t, err)
					require.NoError(t, stream.Close())
					require.Equal(t, "actual shared bytes", string(bytes))
					exact = append(exact, got)
				}
				if outcome == "success" {
					require.Equal(t, exact[0], exact[1])
				}
				require.Equal(t, wf.JobRunCompleted, run(parents[2].JobKey, consumer, tasks[2]).Status, "terminal completion does not require success")
				winner := a.JobID
				if outcome != "success" {
					repair, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", Cell: "test", JobType: "recipe", JobID: "repair", GoalID: a.GoalID, Data: jobdb.NewTaskDataOrPanic(nil)}})
					require.NoError(t, err)
					require.Equal(t, wf.JobRunCompleted, run(repair.JobKey, producer).Status)
					winner = repair.JobKey.JobId
				}
				out := run(parents[3].JobKey, consumer, tasks[3])
				require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
				var got SingleRecipeOutput
				require.NoError(t, json.Unmarshal(out.Output.GetDataOrPanic(), &got))
				require.Equal(t, ResultSource{Goal: a.GoalID, Job: winner}, got.Result)
				require.Equal(t, winner, got.Outputs["producer"])
				cancelled, err := rt.GetJob(ctx, parents[0].JobKey)
				require.NoError(t, err)
				require.Equal(t, jobdb.JobStatusCancelled, cancelled.Status)
				// The explicit goal policy and result source survive replay.
				replay, err := wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("tenant").PlusWorkers(consumer, tasks[3]).BuildEngine()
				require.NoError(t, err)
				_, err = replay.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: parents[3].JobKey})
				require.NoError(t, err, fmt.Sprint(out.JobError))
				// Resume a durable old-format input with omitted waits. Even for a
				// failed original batch it follows the repaired goal, not the new
				// grouped exact-job default. Replay retains the recorded winner.
				legacy := run(parents[5].JobKey, legacyConsumer, tasks[5])
				require.Equal(t, wf.JobRunCompleted, legacy.Status, "%v", legacy.JobError)
				require.NoError(t, json.Unmarshal(legacy.Output.GetDataOrPanic(), &got))
				require.Equal(t, ResultSource{Goal: a.GoalID, Job: winner}, got.Result)
				legacyReplay, err := wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("tenant").PlusWorkers(legacyConsumer, tasks[5]).BuildEngine()
				require.NoError(t, err)
				replayed, err := legacyReplay.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: parents[5].JobKey})
				require.NoError(t, err)
				require.JSONEq(t, string(legacy.Output.GetDataOrPanic()), string(replayed.GetDataOrPanic()))
			})
		}
	}
}
