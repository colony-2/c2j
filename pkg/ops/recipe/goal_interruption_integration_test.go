package recipe

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobdbschema"

	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestGoalWaitTimeoutAndConsumerCancellation(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		for _, cancelled := range []bool{false, true} {
			name := backend + "/timeout"
			if cancelled {
				name = backend + "/cancel"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
				var rt jobdb.WorkflowRuntime = db
				if backend == "http" {
					s := httptest.NewServer(remote.NewServer(db))
					t.Cleanup(s.Close)
					rt, err = remote.New(s.URL, s.Client())
					require.NoError(t, err)
				}
				sdk, err := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
				require.NoError(t, err)
				engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt.(jobdb.JobSchemaRegistry)}
				child, err := sdk.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "t", JobType: "producer", Data: jobdb.NewTaskDataOrPanic(nil)})
				require.NoError(t, err)
				task := &goalWaitTask{ctl: &workerworkflow.SWFWorkflowControl{Engine: engine}, input: StartedJob{JobID: child.JobKey.JobId, GoalID: child.GoalID}, soft: true}
				worker := wf.ExecutionWorker{JobType: "consumer", Execute: func(c wf.ExecutionContext, in wf.JobData) (wf.JobData, error) {
					timeout := time.Second
					if cancelled {
						timeout = time.Minute
					}
					call := wf.TaskCall{Site: "wait", TaskType: task.Name(), ContractVersion: 1, Command: []byte(`{}`), Policy: jobdb.RunPolicy{InvocationTimeout: jobdb.AsDuration(timeout)}}
					return c.DoTask(call, func(context.Context) (jobdb.TaskData, error) { return in, nil })
				}}
				parent, err := sdk.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "t", JobType: worker.Name(), Data: jobdb.NewTaskDataOrPanic(nil)})
				require.NoError(t, err)
				run := func() wf.JobRunOutcome {
					t.Helper()
					r, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: parent.JobKey, JobWorker: worker, TaskWorkers: []wf.TaskWorker{task}, WorkerID: "test", LeaseDuration: time.Minute})
					require.NoError(t, err)
					out, err := r.Run(nil)
					require.NoError(t, err)
					return out
				}
				require.Equal(t, wf.JobRunSuspended, run().Status)
				waits, err := engine.ListDependencyWaits(ctx, jobdb.ListDependencyWaitsRequest{TenantId: "t", ConsumerJobID: parent.JobKey.JobId})
				require.NoError(t, err)
				require.Len(t, waits.Waits, 1)
				key := jobdb.DependencyWaitKey{JobKey: parent.JobKey, WaitID: waits.Waits[0].WaitID}
				want := jobdb.DependencyTimedOut
				if cancelled {
					require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: parent.JobKey}))
					want = jobdb.DependencyCancelled
				} else {
					require.NotNil(t, waits.Waits[0].Deadline)
				}
				require.Eventually(t, func() bool {
					w, e := engine.GetDependencyWait(ctx, key)
					return e == nil && w.State == want
				}, 5*time.Second, 10*time.Millisecond)
				reverse, err := engine.ListDependencyWaits(ctx, jobdb.ListDependencyWaitsRequest{TenantId: "t", Target: &jobdb.DependencyTarget{GoalID: child.GoalID}})
				require.NoError(t, err)
				require.Empty(t, reverse.Waits)
				goal, err := engine.GetGoal(ctx, jobdb.GoalKey{TenantId: "t", GoalID: child.GoalID})
				require.NoError(t, err)
				require.Equal(t, jobdb.GoalOpen, goal.State)
				if !cancelled {
					// A soft goal wait softens goal failure, not the enclosing task timeout.
					out := run()
					require.Equal(t, wf.JobRunFailed, out.Status)
					require.Error(t, out.JobError)
				}
				// A later producer success must not revive the interrupted consumer.
				producer := wf.ExecutionWorker{JobType: "producer", Execute: func(_ wf.ExecutionContext, in wf.JobData) (wf.JobData, error) { return in, nil }}
				r, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: child.JobKey, JobWorker: producer, WorkerID: "producer", LeaseDuration: time.Minute})
				require.NoError(t, err)
				_, err = r.Run(nil)
				require.NoError(t, err)
				retained, err := engine.GetDependencyWait(ctx, key)
				require.NoError(t, err)
				require.Equal(t, want, retained.State)
				require.Empty(t, retained.Dependencies[0].SatisfiedByJobID)
			})
		}
	}
}
