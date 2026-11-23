package executionruntime

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/parentagetest"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type parentageSubmitFunc func(context.Context, jobdb.SubmitJob) (jobref.Ref, error)

func (f parentageSubmitFunc) SubmitJobRef(ctx context.Context, job jobdb.SubmitJob) (jobref.Ref, error) {
	return f(ctx, job)
}

type parentageNoClaims struct{ noClaimsRuntime }

func (r parentageNoClaims) CreateDelegation(ctx context.Context, lease jobdb.ExecutionLease, req jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
	return r.ConsumerRuntime.(jobdb.LeaseDelegator).CreateDelegation(ctx, lease, req)
}
func TestLeaseParentageSuppliedLease(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			var base jobdb.ConsumerRuntime = toy.New(toy.WithProjects(jobdbcelltest.UnrestrictedProjects{}))
			if backend != "toy" {
				db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
				base = db
			}
			if backend == "remote" {
				server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(base)))
				defer server.Close()
				wire, err := remote.New(server.URL, server.Client())
				require.NoError(t, err)
				base = wire
			}
			for _, cell := range []string{"parent", "allowed", "forbidden"} {
				celltest.Create(t, base, "test", cell, "example.test/"+cell)
			}
			require.NoError(t, jobdbschema.Register(ctx, base.(jobdb.JobSchemaRegistry), "test"))
			grant := permissions.JobDB{Jobs: permissions.JobPermissions{Submit: true, CellNames: []string{"allowed"}}}
			parent, err := base.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobID: "parent", JobType: "parent", Cell: "parent", CellRepo: "example.test/parent", Data: jobdb.NewTaskDataOrPanic(nil)}})
			require.NoError(t, err)
			supplied, err := base.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: parent.JobKey, WorkerID: "dispatcher", Routes: []jobdb.Route{{JobType: "parent"}}, LeaseDuration: time.Minute})
			require.NoError(t, err)
			require.NotNil(t, supplied)
			if backend == "remote" {
				capability, err := remote.ExportLease(supplied)
				require.NoError(t, err)
				supplied, err = base.(*remote.Runtime).ImportLease(ctx, capability)
				require.NoError(t, err)
			}
			var guarded jobdb.ConsumerRuntime = noClaimsRuntime{base, t}
			if backend == "remote" {
				guarded = parentageNoClaims{noClaimsRuntime{base, t}}
			}
			rt := New(guarded, execution.Allocation{})
			var saved *permissions.Access
			task := delegationTask(func(local context.Context, tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
				// This is the same controller-mediated access used by worker brokers.
				rt.mu.Lock()
				owner := rt.leases[tc.JobKey]
				rt.mu.Unlock()
				access, scope, stop, err := rt.restrictedAccess(local, tc, owner, grant, nil)
				require.NoError(t, err)
				defer stop()
				saved = access
				current := jobcontext.Current{TenantID: "test", JobID: parent.JobKey.JobId, GoalID: parent.GoalID, InvocationHash: "task-invocation"}
				server, err := childbroker.Start(scope, childbroker.Options{Current: current, Submitter: parentageSubmitFunc(access.Submit)})
				require.NoError(t, err)
				defer server.Close()
				broker, ok, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
				require.NoError(t, err)
				require.True(t, ok)
				rec, err := recipe.LoadRecipeFromString([]byte("id: child\nversion: '1'\nsequence: []\noutputs: {}\n"))
				require.NoError(t, err)
				forged := jobcontext.Parent{TenantID: "test", JobID: "forged", GoalID: "forged", InvocationHash: "forged"}
				req, err := childbroker.NewSubmitRequest(scope, workflowctl.StartJob{TenantId: "test", JobID: "child", Cell: "allowed", CellRepo: "example.test/allowed", RecipeName: "child", Parent: &forged}, nil, *rec)
				require.NoError(t, err)
				wrong := req
				wrong.Start.Cell = "forbidden"
				wrong.Start.CellRepo = "example.test/forbidden"
				_, err = childbroker.Submit(ctx, broker, wrong)
				require.Error(t, err)
				selected := req
				selected.Start.GoalID = parent.GoalID
				_, err = childbroker.PrepareSelected(ctx, broker, selected)
				require.ErrorContains(t, err, "primary goal selection is unsupported")
				_, err = base.GetJob(ctx, jobdb.JobKey{TenantId: "test", JobId: "child"})
				require.ErrorIs(t, err, jobdb.ErrJobNotFound, "rejected preparation must not publish")
				result, err := childbroker.Submit(ctx, broker, req)
				require.NoError(t, err)
				require.Equal(t, "child", result.JobID)
				chapter, err := base.GetChapter(ctx, jobdb.ChapterRef{JobKey: jobdb.JobKey{TenantId: "test", JobId: "child"}, Ordinal: 0})
				require.NoError(t, err)
				var start workflowctl.StartJob
				require.NoError(t, json.Unmarshal(chapter.Body.(jobdb.JobStartChapter).Input.Data, &start))
				expected := jobcontext.ParentFromCurrent(current)
				require.Equal(t, &expected, start.Parent, "authenticated broker replaces caller attribution")
				parentagetest.Check(t, ctx, base, parent.JobKey, "child")
				return in, nil
			})
			worker := wf.ExecutionWorker{JobType: "parent", Execute: func(c wf.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
				return c.DoTask(wf.TaskCall{Site: "submit", TaskType: task.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return in, nil })
			}}
			result, err := rt.RunWithLease(ctx, supplied, wf.GetJobForRunRequest{JobKey: parent.JobKey, JobWorker: worker, TaskWorkers: []wf.TaskWorker{task}}, nil)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunCompleted, result.Status, "%v", result.JobError)
			require.NotNil(t, saved)
			_, err = saved.Submit(ctx, jobdb.SubmitJob{TenantId: "test", JobID: "after-task", JobType: "child", Cell: "allowed", CellRepo: "example.test/allowed", Data: jobdb.NewTaskDataOrPanic(nil)})
			require.Error(t, err, "closed task cannot publish")
			parentagetest.Check(t, ctx, base, parent.JobKey, "child")
		})
	}
}
