package executionruntime

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestGroupedLeaseAuthorityAndHookCeiling(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
			var base jobdb.ConsumerRuntime = db
			if backend == "http" {
				s := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(db)))
				t.Cleanup(s.Close)
				base, err = remote.New(s.URL, s.Client())
				require.NoError(t, err)
			}
			rt := New(base, execution.Allocation{})
			celltest.Create(t, rt, "tenant", "test", "example.test/test")
			wide := permissions.JobDB{Jobs: permissions.JobPermissions{Submit: true}, Secrets: permissions.SecretPermissions{Resolve: []string{"stolen"}}}
			start := workflowctl.StartJob{TenantId: "tenant", Cell: "test", RecipeName: "batch", JobContext: contextual.JobContext{InheritedPermissions: &permissions.State{Version: 1, Grant: &wide}}}
			q := jobdb.StartOrAppendJob{AppendKey: "authority", Append: json.RawMessage(`{"content":{},"artifacts":[]}`), Job: jobdb.SubmitJob{TenantId: "tenant", Cell: "test", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(start)}}
			require.NoError(t, jobdbschema.SetSubmitJobSchema(&q.Job))
			var saved *permissions.Access
			task := delegationTask(func(local context.Context, tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
				rt.mu.Lock()
				owner := rt.leases[tc.JobKey]
				rt.mu.Unlock()
				denied, _, stop, err := rt.restrictedAccess(local, tc, owner, permissions.JobDB{}, nil)
				require.NoError(t, err)
				defer stop()
				_, err = denied.StartOrAppend(local, q)
				require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
				grant := permissions.JobDB{Jobs: permissions.JobPermissions{Submit: true, CellNames: []string{"test"}}, Secrets: permissions.SecretPermissions{Resolve: []string{"allowed"}}}
				access, _, stop, err := rt.restrictedAccess(local, tc, owner, grant, nil)
				require.NoError(t, err)
				defer stop()
				saved = access
				wrong := q
				wrong.Job.Cell = "other"
				_, err = access.StartOrAppend(local, wrong)
				require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
				wrong = q
				wrong.Job.TenantId = "foreign"
				_, err = access.StartOrAppend(local, wrong)
				require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
				hooked, _, stopHook, err := rt.restrictedAccess(local, tc, owner, grant, nil, permissions.State{Version: 1, ChildPolicy: json.RawMessage(`{"revision":"1"}`)})
				require.NoError(t, err)
				defer stopHook()
				hookReq := q
				hookReq.AppendKey = "hook"
				h, err := hooked.StartOrAppend(local, hookReq)
				require.NoError(t, err)
				chapters, err := base.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: h.Key()})
				require.NoError(t, err)
				require.Len(t, chapters, 1)
				body := chapters[0].Body.(jobdb.JobStartChapter)
				var captured workflowctl.StartJob
				require.NoError(t, json.Unmarshal(body.Input.Data, &captured))
				require.Nil(t, captured.Parent)
				require.Equal(t, grant, *captured.JobContext.InheritedPermissions.Limit)
				require.Error(t, captured.JobContext.InheritedPermissions.CheckInput("stolen"))
				require.NoError(t, captured.JobContext.InheritedPermissions.CheckInput("allowed"))
				a, err := access.StartOrAppend(local, q)
				if err != nil {
					return nil, err
				}
				b, err := access.StartOrAppend(local, q)
				require.NoError(t, err)
				require.Equal(t, a, b)
				require.NotEmpty(t, a.CellID)
				require.Equal(t, "test", a.Cell)
				require.Equal(t, "tenant", a.TenantID)
				require.NotEmpty(t, a.GoalID)
				_, err = access.StartOrAppend(local, wrong)
				require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
				accepted, err := base.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: a.Key()})
				require.NoError(t, err)
				require.Len(t, accepted, 2, "foreign tenant must not add a contribution after schema registration")
				return in, nil
			})
			parent, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", Cell: "test", JobType: "parent", Data: jobdb.NewTaskDataOrPanic(nil)}})
			require.NoError(t, err)
			worker := wf.ExecutionWorker{JobType: "parent", Execute: func(c wf.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
				return c.DoTask(wf.TaskCall{Site: "submit", TaskType: task.Name(), ContractVersion: 1, Command: []byte(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return in, nil })
			}}
			h, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: parent.JobKey, JobWorker: worker, TaskWorkers: []wf.TaskWorker{rt.WrapTaskWorker(task)}, WorkerID: "parent", LeaseDuration: time.Minute})
			require.NoError(t, err)
			out, err := h.Run(nil)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
			_, err = saved.StartOrAppend(ctx, q)
			require.Error(t, err, "completed task must not reuse submission authority")
			jobs, err := base.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, JobTypes: []string{"recipe"}})
			require.NoError(t, err)
			require.Len(t, jobs.Jobs, 2)
			for _, job := range jobs.Jobs {
				require.Empty(t, job.ParentJobID)
			}
		})
	}
}
