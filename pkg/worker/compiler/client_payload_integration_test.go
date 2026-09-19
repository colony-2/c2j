package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Exercise our context wrappers against the actual migration contract, including
// an external completion that does not echo or update the client payload.
func TestClientPayloadSurvivesYieldAndExternalTaskCompletion(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var rt jobdb.WorkflowRuntime
			if backend == "toy" {
				rt = toyruntime.New()
			} else {
				db, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close(context.Background())) })
				rt = db
			}
			worker := payloadLifecycleWorker{}
			handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{
				TenantId: "tenant", JobID: "payload", JobType: worker.Name(),
				Data:                jobdb.NewTaskDataOrPanic(map[string]any{"input": true}),
				RunPolicy:           jobdb.DefaultRunPolicy(),
				ClientPayloadUpdate: &jobdb.ClientPayloadUpdate{Mode: "reset", Value: json.RawMessage(`{"run_policy":"client-owned","large":9007199254740993}`)},
			}})
			require.NoError(t, err)
			run := func() jobworkflow.JobRunOutcome {
				runnable, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{
					JobKey: handle.JobKey, JobWorker: worker, WorkerID: "test", LeaseDuration: time.Minute,
				})
				require.NoError(t, err)
				outcome, err := runnable.Run(nil)
				require.NoError(t, err)
				return outcome
			}
			first := run()
			require.Equal(t, jobworkflow.JobRunSuspended, first.Status)
			require.Equal(t, &jobdb.Route{JobType: worker.Name()}, first.NextRoute)
			chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
			require.NoError(t, err)
			require.Len(t, chapters, 1, "explicit yield must not write a task/job result chapter")
			waiting := run()
			require.Equal(t, jobworkflow.JobRunSuspended, waiting.Status)
			require.Equal(t, &jobdb.Route{JobType: worker.Name(), TaskType: "external:review"}, waiting.NextRoute)
			require.Equal(t, waiting.NextRoute, waiting.MissingRoute)
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
			require.NoError(t, err)
			task, err := engine.GetWaitingTask(ctx, handle.JobKey)
			require.NoError(t, err)
			require.Equal(t, "external:review", task.TaskType())
			found, err := engine.FindTasksWaitingForRoute(ctx, worker.Name(), "external:review", []string{"tenant"})
			require.NoError(t, err)
			require.Len(t, found, 1)
			require.NoError(t, task.Finish(ctx, jobdb.NewTaskDataOrPanic(map[string]any{"done": true})))
			require.Equal(t, jobworkflow.JobRunCompleted, run().Status)
			info, err := rt.GetJob(ctx, handle.JobKey)
			require.NoError(t, err)
			require.EqualValues(t, 2, info.ClientPayloadRevision)
			var payload map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(info.ClientPayload, &payload))
			require.Equal(t, `9007199254740993`, string(payload["large"]))
			require.Equal(t, `"client-owned"`, string(payload["run_policy"]))
			require.Equal(t, `"resumed"`, string(payload["phase"]))
			chapters, err = rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
			require.NoError(t, err)
			var taskChapters int
			for _, chapter := range chapters {
				if chapter.TaskType == "external:review" {
					taskChapters++
				}
			}
			require.Equal(t, 1, taskChapters, "external completion retains the original task identifier and replay does not duplicate its result")
		})
	}
}

type payloadLifecycleWorker struct{}

func (payloadLifecycleWorker) Name() string { return "payload:lifecycle" }
func (w payloadLifecycleWorker) Run(ctx jobworkflow.JobContext, _ jobdb.JobData) (jobdb.JobData, error) {
	wrapped := newThinPackForwardingJobContext(withExecutionTimeout(ctx, time.Minute, "payload test"))
	if revision := wrapped.ClientPayloadRevision(); revision == 1 {
		if err := wrapped.Yield(context.Background(), jobdb.RescheduleExecutionRequest{
			NextRoute:           jobdb.Route{JobType: w.Name()},
			ClientPayloadUpdate: &jobdb.ClientPayloadUpdate{Mode: "patch", ExpectedRevision: &revision, Value: json.RawMessage(`{"phase":"resumed"}`)},
		}); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("successful yield must stop the invocation")
	}
	if wrapped.ClientPayloadRevision() != 2 {
		return nil, fmt.Errorf("unexpected client payload revision %d", wrapped.ClientPayloadRevision())
	}
	return wrapped.DoTask(jobdb.DefaultRunPolicy(), "external:review", jobdb.NewTaskDataOrPanic(map[string]any{"prompt": true}))
}
