package executionruntime

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestCellCreationThroughRestrictedTaskLease(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			var base jobdb.WorkflowRuntime = toy.New()
			if backend != "toy" {
				db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
				base = db
			}
			if backend == "remote" {
				server := httptest.NewServer(celltest.Handler(remote.NewServer(base)))
				t.Cleanup(server.Close)
				client, err := remote.New(server.URL, server.Client())
				require.NoError(t, err)
				base = client
			}
			rt := New(base, execution.Allocation{SchemaVersion: 2})
			var saved *permissions.Access
			var delegated jobdb.ExecutionLease
			task := delegationTask(func(local context.Context, tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
				access := permissions.FromContext(local)
				require.NotNil(t, access)
				saved = access
				_, err := access.CreateCell(local, jobdb.CreateCellRequest{TenantID: "tenant", Name: "denied", Repo: "example.com/denied"})
				require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
				req := jobdb.CreateCellRequest{TenantID: "tenant", Name: "payments", Repo: "example.com/payments", RequestID: "stable-create"}
				first, err := access.CreateCell(local, req)
				require.NoError(t, err)
				retry, err := access.CreateCell(local, req)
				require.NoError(t, err)
				require.Equal(t, first.ID, retry.ID)
				req.TenantID = "other"
				_, err = access.CreateCell(local, req)
				require.Error(t, err)
				_, err = access.Submit(local, jobdb.SubmitJob{TenantId: "tenant", Cell: "payments"})
				require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
				if backend == "remote" {
					delegated, err = (permissions.JobDBIssuer{}).IssueRestrictedLease(local, tc, access.Grant(), time.Time{})
					require.NoError(t, err)
					_, err = delegated.CreateCell(local, jobdb.CreateCellRequest{Name: "denied", Repo: "example.com/denied"})
					require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied, "server enforces the name selector")
				}
				return in, nil
			})
			worker := wf.ExecutionWorker{JobType: "creation", Execute: func(c wf.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
				return c.DoTask(wf.TaskCall{Site: "create", TaskType: task.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return in, nil })
			}}
			celltest.Create(t, base, "tenant", "parent", "example.com/parent")
			policy := permissions.State{Version: permissions.Version, Grant: &permissions.JobDB{Cells: permissions.CellPermissions{Create: true, CellNames: []string{"payments"}}}}
			handle, err := base.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: worker.Name(), Cell: "parent", Data: jobdb.NewTaskDataOrPanic(map[string]any{"permissions": policy})}})
			require.NoError(t, err)
			run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: worker, TaskWorkers: []wf.TaskWorker{rt.WrapTaskWorker(task)}, WorkerID: "worker", LeaseDuration: time.Minute})
			require.NoError(t, err)
			out, err := run.Run(nil)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunCompleted, out.Status, "%+v", out)
			_, err = saved.CreateCell(ctx, jobdb.CreateCellRequest{Name: "payments", Repo: "example.com/payments"})
			require.Error(t, err, "completed task cannot retain creation access")
			if delegated != nil {
				_, err = delegated.CreateCell(ctx, jobdb.CreateCellRequest{Name: "payments", Repo: "example.com/payments"})
				require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
			}
			cells, err := base.ListCells(ctx, jobdb.ListCellsRequest{TenantID: "tenant"})
			require.NoError(t, err)
			require.Len(t, cells.Cells, 2)
		})
	}
}
