package executionruntime

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceLookupIndependentOfSubmission(t *testing.T) {
	for _, backend := range []string{"embedded", "remote"} {
		for _, policy := range []string{"empty", "submit-own-cell"} {
			t.Run(backend+"/"+policy, func(t *testing.T) {
				ctx := celltest.Context(context.Background())
				var base jobdb.WorkflowRuntime = toy.New()
				if backend == "remote" {
					server := httptest.NewServer(celltest.Handler(remote.NewServer(base)))
					t.Cleanup(server.Close)
					client, err := remote.New(server.URL, server.Client())
					require.NoError(t, err)
					base = client
				}
				celltest.Create(t, base, "test", "A", "example.test/A")
				celltest.Create(t, base, "test", "B", "example.test/B")
				grant := permissions.JobDB{}
				if policy == "submit-own-cell" {
					grant.Jobs = permissions.JobPermissions{Submit: true, CellNames: []string{"A"}}
				}
				rt := New(base, execution.Allocation{})
				var saved jobref.Scope
				var delegated jobdb.ExecutionLease
				task := delegationTask(func(local context.Context, tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
					scope, ok := jobref.FromContext(local)
					require.True(t, ok)
					saved = scope
					target, err := scope.LookupCell(local, "B")
					require.NoError(t, err)
					require.Equal(t, "B", target.Cell)
					require.Equal(t, "example.test/B", target.CellRepo)
					require.NotEmpty(t, target.CellID)
					_, err = scope.ResolveCell(local, "B")
					require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
					job := jobdb.SubmitJob{TenantId: "test", Cell: "B", CellRepo: target.CellRepo, JobType: "child", Data: jobdb.NewTaskDataOrPanic(nil)}
					_, err = scope.Submit(local, job)
					require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
					_, err = scope.LookupCell(local, "missing")
					require.ErrorIs(t, err, jobdb.ErrCellNotFound)
					if backend == "remote" {
						delegated, err = (permissions.JobDBIssuer{}).IssueRestrictedLease(local, tc, grant, time.Time{})
						require.NoError(t, err)
						lookedUp, err := delegated.LookupCell(local, "B")
						require.NoError(t, err)
						require.Equal(t, target, lookedUp)
						_, err = delegated.SubmitJob(local, jobdb.SubmitJobRequest{Job: job})
						require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
					}
					return in, nil
				})
				worker := wf.ExecutionWorker{JobType: "lookup", Execute: func(c wf.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
					return c.DoTask(wf.TaskCall{Site: "lookup", TaskType: task.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return in, nil })
				}}
				in := jobdb.NewTaskDataOrPanic(map[string]any{"permissions": permissions.State{Version: permissions.Version, Grant: &grant}})
				h, err := base.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", Cell: "A", CellRepo: "example.test/A", JobType: worker.Name(), Data: in}})
				require.NoError(t, err)
				run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: worker, TaskWorkers: []wf.TaskWorker{rt.WrapTaskWorker(task)}, WorkerID: "lookup", LeaseDuration: time.Minute})
				require.NoError(t, err)
				out, err := run.Run(nil)
				require.NoError(t, err)
				require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
				_, err = saved.LookupCell(ctx, "B")
				require.Error(t, err, "closed task scope cannot retain lookup access")
				if delegated != nil {
					_, err = delegated.LookupCell(ctx, "B")
					require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
				}
			})
		}
	}
}
