package recipe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/segmentio/ksuid"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type exactWaitTask struct {
	ctl   *workerworkflow.SWFWorkflowControl
	input StartedJob
	mode  string
	calls atomic.Int32
}

func (*exactWaitTask) Name() string { return "exact-wait" }
func (task *exactWaitTask) Run(c wf.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	task.calls.Add(1)
	deps := ops.NewOpDependenciesBuilder().WithWorkflowControl(task.ctl).WithJobTool(&ops.TaskBasedJobTool{TaskContext: c}).Build()
	switch task.mode {
	case "soft":
		out, err := awaitResultSoft(deps, c.Context(), AwaitResultSoftInput{Target: WaitTarget{Job: task.input.JobID}})
		if err != nil {
			return nil, err
		}
		return jobdb.NewTaskData(out)
	case "complete":
		binding, err := awaitChild(deps, c.Context(), task.input, false)
		if err != nil {
			return nil, err
		}
		return jobdb.NewTaskData(binding)
	default:
		out, err := waitAndGetRecipeOutput(deps, c.Context(), task.input)
		if err != nil {
			return nil, err
		}
		return jobdb.NewTaskData(out)
	}
}

// CJ03–CJ07: real legacy backends, including durable suspension/reopen over
// native SQLite and its HTTP transport. B is deliberately in A's old goal.
func TestExactJobBackendIsolation(t *testing.T) {
	backends := []string{"toy", "sqlite", "remote"}
	if os.Getenv("C2J_EXACT_JOB_TEST_URL") != "" {
		backends = append(backends, "postgres")
	}
	for _, backend := range backends {
		for _, ending := range []string{"cancelled", "failed"} {
			for _, mode := range []string{"complete", "soft", "result"} {
				t.Run(fmt.Sprintf("%s/%s/%s", backend, ending, mode), func(t *testing.T) {
					tenant := "tenant"
					if backend == "postgres" {
						tenant = ksuid.New().String()
					}
					ctx := jobdb.WithPrimaryGoalSelectionAuthorizer(celltest.Context(context.Background()), func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error { return nil })
					var rt jobdb.ConsumerRuntime
					path := filepath.Join(t.TempDir(), "jobs.db")
					// Keep the concrete close operation in the reopen closure rather than
					// requiring an enlarged runtime interface.
					var closeDB func()
					var server *httptest.Server
					open := func() {
						var err error
						if backend == "postgres" {
							rt, err = remote.New(os.Getenv("C2J_EXACT_JOB_TEST_URL"), nil)
							require.NoError(t, err)
							return
						}
						if backend == "toy" {
							if rt == nil {
								rt = toy.New()
							}
							return
						}
						local, e := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: path})
						require.NoError(t, e)
						closeDB = func() { require.NoError(t, local.Close(ctx)) }
						rt = local
						if backend == "remote" {
							handler := remote.NewConsumerServer(local)
							server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
								handler.ServeHTTP(w, r.WithContext(jobdb.WithPrimaryGoalSelectionAuthorizer(celltest.Context(r.Context()), func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error { return nil })))
							}))
							rt, err = remote.New(server.URL, server.Client())
							require.NoError(t, err)
						}
					}
					closeRuntime := func() {
						if server != nil {
							server.Close()
							server = nil
						}
						if closeDB != nil {
							closeDB()
							closeDB = nil
						}
					}
					open()
					t.Cleanup(closeRuntime)
					celltest.Create(t, rt, tenant, "test", "example.test/test")
					submit := func(id, kind, goal string) jobdb.JobHandle {
						h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: tenant, JobID: id, GoalID: goal, JobType: kind, Data: jobdb.NewTaskDataOrPanic(nil), Cell: "test", CellRepo: "example.test/test"}})
						require.NoError(t, err)
						return h
					}
					producer := wf.ExecutionWorker{JobType: "producer", Execute: func(c wf.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
						data := jobdb.NewTaskDataOrPanic(map[string]any{"output": map[string]any{"producer": c.GetJobKey().JobId, "n": json.Number("9007199254740993")}}, jobdb.NewArtifactFromBytes("result.txt", []byte(c.GetJobKey().JobId)))
						if c.GetJobKey().JobId == "A" {
							return data, errors.New("A failed")
						}
						return data, nil
					}}
					a := submit("A", producer.Name(), "")
					task := &exactWaitTask{input: StartedJob{JobID: "A", GoalID: a.GoalID}, mode: mode}
					consumer := wf.ExecutionWorker{JobType: "consumer", Execute: func(c wf.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
						return c.DoTask(wf.TaskCall{Site: "wait", TaskType: task.Name(), ContractVersion: 1, Command: []byte(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return in, nil })
					}}
					parent := submit("consumer", consumer.Name(), "")
					configure := func() {
						engine, err := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
						require.NoError(t, err)
						task.ctl = &workerworkflow.SWFWorkflowControl{Engine: jobdbschema.WorkflowEngine{Engine: engine, Registry: rt.(jobdb.JobSchemaRegistry)}}
					}
					configure()
					run := func(h jobdb.JobHandle, w wf.ExecutionWorker, tasks ...wf.TaskWorker) wf.JobRunOutcome {
						runner, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: w, TaskWorkers: tasks, WorkerID: "test", LeaseDuration: time.Minute})
						require.NoError(t, err)
						out, err := runner.Run(nil)
						require.NoError(t, err)
						return out
					}
					require.Equal(t, wf.JobRunSuspended, run(parent, consumer, task).Status)
					if backend != "toy" {
						closeRuntime()
						open()
						configure()
					}
					pending, err := rt.GetJob(ctx, parent.JobKey)
					require.NoError(t, err)
					require.NotNil(t, pending.BlockingWait)
					require.Equal(t, jobdb.DependencyTarget{JobID: "A"}, pending.BlockingWait.Dependencies[0].Target)
					if ending == "cancelled" {
						require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: a.JobKey}))
					} else {
						require.Equal(t, wf.JobRunFailed, run(a, producer).Status)
					}
					b := submit("B", producer.Name(), a.GoalID)
					require.Equal(t, wf.JobRunCompleted, run(b, producer).Status)
					out := run(parent, consumer, task)
					if mode == "result" {
						require.Equal(t, wf.JobRunFailed, out.Status)
						require.Error(t, out.JobError)
					} else {
						require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
						if mode == "soft" {
							var got ChildStatusOutput
							require.NoError(t, json.Unmarshal(out.Output.GetDataOrPanic(), &got))
							require.Equal(t, "A", got.Result.Job)
							require.Equal(t, ending, got.Status)
							require.True(t, got.Terminal)
						} else {
							var got jobdb.DependencyResolution
							require.NoError(t, json.Unmarshal(out.Output.GetDataOrPanic(), &got))
							require.Equal(t, jobdb.DependencyTarget{JobID: "A"}, got.Target)
							require.Equal(t, "A", got.SatisfiedByJobID)
						}
					}
					waitKey := jobdb.DependencyWaitKey{JobKey: parent.JobKey, WaitID: pending.BlockingWait.WaitID}
					before, err := rt.GetDependencyWait(ctx, waitKey)
					require.NoError(t, err)
					// Only an explicitly new target can collect B's output and artifact.
					task.input = StartedJob{JobID: "B", GoalID: b.GoalID}
					task.mode = "result"
					second := submit("explicit-B", consumer.Name(), "")
					result := run(second, consumer, task)
					// Registration checkpoints even an already terminal
					// dependency; a fresh lease consumes its settled witness.
					require.Equal(t, wf.JobRunSuspended, result.Status)
					registered, err := rt.ListDependencyWaits(ctx, jobdb.ListDependencyWaitsRequest{TenantId: tenant, ConsumerJobID: second.JobKey.JobId})
					require.NoError(t, err)
					require.Len(t, registered.Waits, 1)
					require.Equal(t, jobdb.DependencyTarget{JobID: "B"}, registered.Waits[0].Dependencies[0].Target)
					result = run(second, consumer, task)
					require.Equal(t, wf.JobRunCompleted, result.Status, "%v", result.JobError)
					require.Contains(t, string(result.Output.GetDataOrPanic()), "9007199254740993")
					var got SingleRecipeOutput
					require.NoError(t, json.Unmarshal(result.Output.GetDataOrPanic(), &got))
					require.Equal(t, ResultSource{Job: "B"}, got.Result)
					require.Equal(t, "B", got.Outputs["producer"])
					require.NotNil(t, got.Artifacts["result.txt"].Stored)
					require.Equal(t, "B", got.Artifacts["result.txt"].Stored.Key.JobId)
					artifact := task.ctl.GetArtifactLazy(ctx, tenant, got.Artifacts["result.txt"].Stored.Key)
					bytes, err := artifact.Bytes(ctx)
					require.NoError(t, err)
					require.Equal(t, []byte("B"), bytes)
					after, err := rt.GetDependencyWait(ctx, waitKey)
					require.NoError(t, err)
					require.Equal(t, before, after)
				})
			}
		}
	}
}
