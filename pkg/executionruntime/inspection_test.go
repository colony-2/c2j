package executionruntime

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestTaskInspectionReadsTerminalChildWithoutSubmitGrant(t *testing.T) {
	for _, backend := range []string{"toy", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			var base jobdb.WorkflowRuntime = toy.New()
			if backend == "remote" {
				s := httptest.NewServer(celltest.Handler(remote.NewServer(base)))
				defer s.Close()
				client, e := remote.New(s.URL, s.Client())
				require.NoError(t, e)
				base = client
			}
			rt := New(base, execution.Allocation{})
			celltest.Create(t, rt, "tenant", "cell", "example.test/cell")
			submit := func(typ string, meta []byte) jobdb.JobHandle {
				h, e := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: typ, Cell: "cell", CellRepo: "example.test/cell", Data: jobdb.NewTaskDataOrPanic(nil), Metadata: meta}})
				require.NoError(t, e)
				return h
			}
			parent := submit("parent", nil)
			other := submit("other", nil)
			meta, e := json.Marshal(starter.JobMetadata{ParentTenantID: "tenant", ParentJobID: parent.JobKey.JobId})
			require.NoError(t, e)
			pretender := submit("other", meta)
			var child jobdb.JobHandle
			childWorker := wf.ExecutionWorker{JobType: "child", Execute: func(wf.ExecutionContext, jobdb.JobData) (jobdb.JobData, error) {
				return jobdb.NewTaskDataOrPanic(map[string]any{"answer": 42}), nil
			}}
			var saved jobinspect.API
			task := delegationTask(func(local context.Context, tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
				rt.mu.Lock()
				owner := rt.leases[tc.JobKey]
				rt.mu.Unlock()
				// Fixture setup uses the owner lease so JobDB records genuine
				// lineage; the inspection scope below has no submission grant.
				var e error
				child, e = owner.ExecutionLease.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: "child", Cell: "cell", CellRepo: "example.test/cell", Data: jobdb.NewTaskDataOrPanic(nil)}})
				require.NoError(t, e)
				childRun, e := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: child.JobKey, JobWorker: childWorker, WorkerID: "child", LeaseDuration: time.Minute})
				require.NoError(t, e)
				_, e = childRun.Run(nil)
				require.NoError(t, e)
				access, scope, cancel, e := rt.restrictedAccess(local, tc, owner, permissions.JobDB{}, nil)
				require.NoError(t, e)
				defer cancel()
				api := jobinspect.FromContext(scope)
				require.NotNil(t, api)
				saved = api
				_, e = api.Inspect(ctx, jobinspect.Request{Operation: "show", JobID: parent.JobKey.JobId})
				require.NoError(t, e)
				p := "/answer"
				r, e := api.Inspect(ctx, jobinspect.Request{Operation: "result", JobID: child.JobKey.JobId, Field: &p})
				require.NoError(t, e)
				require.Equal(t, "42", string(r.Value))
				listing, e := api.Inspect(ctx, jobinspect.Request{Operation: "list"})
				require.NoError(t, e)
				require.Len(t, listing.Jobs, 1)
				require.Equal(t, child.JobKey.JobId, listing.Jobs[0].ID)
				_, e = api.Inspect(ctx, jobinspect.Request{Operation: "show", JobID: other.JobKey.JobId})
				require.ErrorIs(t, e, jobdb.ErrLeasePermissionDenied)
				_, e = api.Inspect(ctx, jobinspect.Request{Operation: "show", JobID: pretender.JobKey.JobId})
				require.ErrorIs(t, e, jobdb.ErrLeasePermissionDenied)
				current := jobcontext.Current{TenantID: "tenant", JobID: parent.JobKey.JobId, GoalID: parent.GoalID}
				server, e := childbroker.Start(scope, childbroker.Options{Current: current, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
				require.NoError(t, e)
				defer server.Close()
				broker, _, e := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
				require.NoError(t, e)
				client := childbroker.JobClient{Broker: broker}
				r, e = client.Inspect(ctx, jobinspect.Request{Operation: "result", JobID: child.JobKey.JobId, Field: &p})
				require.NoError(t, e)
				require.Equal(t, "42", string(r.Value))
				_, e = client.Inspect(ctx, jobinspect.Request{Operation: "show", JobID: other.JobKey.JobId})
				require.Error(t, e)
				stop := access.LimitDeadline(time.Now().Add(-time.Second))
				defer stop()
				_, e = api.Inspect(ctx, jobinspect.Request{Operation: "show", JobID: parent.JobKey.JobId})
				require.ErrorIs(t, e, context.DeadlineExceeded)
				return in, nil
			})
			worker := wf.ExecutionWorker{JobType: "parent", Execute: func(c wf.ExecutionContext, data jobdb.JobData) (jobdb.JobData, error) {
				return c.DoTask(wf.TaskCall{Site: "inspect", TaskType: task.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return data, nil })
			}}
			run, e := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: parent.JobKey, JobWorker: worker, TaskWorkers: []wf.TaskWorker{rt.WrapTaskWorker(task)}, WorkerID: "parent", LeaseDuration: time.Minute})
			require.NoError(t, e)
			out, e := run.Run(nil)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunCompleted, out.Status)
			_, e = saved.Inspect(ctx, jobinspect.Request{Operation: "show", JobID: child.JobKey.JobId})
			require.Error(t, e)
		})
	}
}
