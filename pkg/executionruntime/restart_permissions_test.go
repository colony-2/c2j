package executionruntime

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobrestart"
	restartservice "github.com/colony-2/c2j/pkg/jobrestart/service"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestLeaseBoundRestartHelperSubmission(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
			require.NoError(t, err)
			defer db.Close(ctx)
			var base jobdb.WorkflowRuntime = db
			if backend == "remote" {
				server := httptest.NewServer(celltest.Handler(remote.NewServer(db)))
				defer server.Close()
				base, err = remote.New(server.URL, server.Client())
				require.NoError(t, err)
			}
			rt := New(base, execution.Allocation{})
			celltest.Create(t, rt, "tenant", "cell", "example.test/cell")
			submit := func(typ string) jobdb.JobHandle {
				h, e := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: typ, Cell: "cell", CellRepo: "example.test/cell", Data: jobdb.NewTaskDataOrPanic(nil)}})
				require.NoError(t, e)
				return h
			}
			source, e := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: "recipe", Cell: "cell", CellRepo: "example.test/cell", Data: jobdb.NewTaskDataOrPanic(workflowctl.StartJob{TenantId: "tenant", RecipeName: "fixture", Cell: "cell", CellRepo: "example.test/cell"})}})
			require.NoError(t, e)
			sourceWorker := wf.ExecutionWorker{JobType: "recipe", Execute: func(wf.ExecutionContext, jobdb.JobData) (jobdb.JobData, error) {
				return jobdb.NewTaskDataOrPanic(map[string]any{"value": "original"}), nil
			}}
			sourceRun, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: source.JobKey, JobWorker: sourceWorker, WorkerID: "source", LeaseDuration: time.Minute})
			require.NoError(t, err)
			_, err = sourceRun.Run(nil)
			require.NoError(t, err)
			before, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: source.JobKey})
			require.NoError(t, err)
			request := jobdb.SubmitRestartJobRequest{RequestID: "restart-through-helper", Job: jobdb.SubmitRestartJob{Cell: "cell", PriorJobKey: source.JobKey, LastStepToKeep: 0}}
			engine, e := wf.NewEngineBuilder().WithRuntime(base).BuildEngine()
			require.NoError(t, e)
			rt.RestartAPI = &restartservice.Service{Engine: engine, Tenant: "tenant", Submit: (restartservice.TaskSubmitter{Engine: engine, Registry: base.(jobdb.JobSchemaRegistry)}).Submit}
			zero := int64(0)
			helperRequest := jobrestart.Request{Operation: "submit", JobID: source.JobKey.JobId, Selector: jobrestart.Selector{Chapter: &zero}, Goal: "new", RequestID: "restart-through-helper"}

			var saved jobrestart.API
			task := delegationTask(func(local context.Context, tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
				rt.mu.Lock()
				owner := rt.leases[tc.JobKey]
				rt.mu.Unlock()
				denied, _, cancel, e := rt.restrictedAccess(local, tc, owner, permissions.JobDB{}, nil)
				require.NoError(t, e)
				defer cancel()
				_, e = denied.Restart(ctx, request)
				require.ErrorIs(t, e, jobdb.ErrLeasePermissionDenied)
				grant := permissions.JobDB{Jobs: permissions.JobPermissions{Submit: true, CellNames: []string{"cell"}}}
				access, scope, cancel, e := rt.restrictedAccess(local, tc, owner, grant, nil)
				require.NoError(t, e)
				defer cancel()
				saved = jobrestart.FromContext(scope)
				wrong := request
				wrong.Job.Cell = "other"
				_, e = access.Restart(ctx, wrong)
				require.ErrorIs(t, e, jobdb.ErrLeasePermissionDenied)
				server, e := childbroker.Start(scope, childbroker.Options{Current: jobcontext.Current{TenantID: "tenant", JobID: tc.JobKey.JobId, GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
				require.NoError(t, e)
				defer server.Close()
				broker, _, e := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
				require.NoError(t, e)
				client := childbroker.JobClient{Broker: broker}
				result, e := client.Restart(ctx, helperRequest)
				require.NoError(t, e)
				require.NotNil(t, result.Ref)
				require.NoError(t, result.Ref.Validate())
				family, e := jobinspect.ReadDAG(ctx, client, result.Ref.JobID, jobinspect.DefaultDAGOptions())
				require.NoError(t, e)
				require.True(t, family.Coverage.Complete)
				require.Equal(t, tc.JobKey.JobId, family.Root.ID)
				require.Contains(t, family.Edges, jobinspect.SubmissionEdge{Type: "submission", Parent: tc.JobKey.JobId, Child: result.Ref.JobID})
				require.Equal(t, "tenant", result.Ref.TenantID)
				retry, e := client.Restart(ctx, helperRequest)
				require.NoError(t, e)
				require.Equal(t, result.Ref, retry.Ref)
				require.Len(t, server.StartedJobs().Items, 2, "helper records restart identities for operation output")
				wrongTenant := request
				wrongTenant.Job.PriorJobKey.TenantId = "other"
				_, e = access.Restart(ctx, wrongTenant)
				require.ErrorIs(t, e, jobdb.ErrLeasePermissionDenied)
				hooked, _, stopHook, e := rt.restrictedAccess(local, tc, owner, grant, nil, permissions.State{ChildPolicy: json.RawMessage(`{}`)})
				require.NoError(t, e)
				defer stopHook()
				_, e = hooked.Restart(ctx, request)
				require.ErrorContains(t, e, "inherited hook policy")

				after, e := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: source.JobKey})
				require.NoError(t, e)
				require.Equal(t, before, after)
				stop := access.LimitDeadline(time.Now().Add(-time.Second))
				defer stop()
				_, e = saved.Restart(ctx, jobrestart.Request{Operation: "points", JobID: source.JobKey.JobId})
				require.ErrorIs(t, e, context.DeadlineExceeded)
				return in, nil
			})
			parent := submit("parent")
			worker := wf.ExecutionWorker{JobType: "parent", Execute: func(c wf.ExecutionContext, data jobdb.JobData) (jobdb.JobData, error) {
				return c.DoTask(wf.TaskCall{Site: "restart", TaskType: task.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return data, nil })
			}}
			run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: parent.JobKey, JobWorker: worker, TaskWorkers: []wf.TaskWorker{rt.WrapTaskWorker(task)}, WorkerID: "parent", LeaseDuration: time.Minute})
			require.NoError(t, err)
			_, err = run.Run(nil)
			require.NoError(t, err)
			_, err = saved.Restart(ctx, jobrestart.Request{Operation: "points", JobID: source.JobKey.JobId})
			require.Error(t, err)
		})
	}
}
