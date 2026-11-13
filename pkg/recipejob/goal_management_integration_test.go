package recipejob_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestGoalManagementLifecycle(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx := jobdb.WithPrimaryGoalSelectionAuthorizer(celltest.Context(context.Background()), func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error { return nil })
			db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
			var rt jobdb.WorkflowRuntime = db
			if backend == "http" {
				handler := remote.NewServer(db)
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					handler.ServeHTTP(w, r.WithContext(jobdb.WithPrimaryGoalSelectionAuthorizer(celltest.Context(r.Context()), func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error { return nil })))
				}))
				t.Cleanup(s.Close)
				rt, err = remote.New(s.URL, s.Client())
				require.NoError(t, err)
			}
			sdk, err := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
			require.NoError(t, err)
			engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt.(jobdb.JobSchemaRegistry)}
			submit := func(id, goal string, deps ...jobdb.Dependency) jobdb.JobHandle {
				t.Helper()
				celltest.Create(t, rt.(jobdb.JobSchemaRegistry), "tenant", "test", "example.test/test")
				h, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "tenant", JobID: id, GoalID: goal, JobType: "candidate", Data: jobdb.NewTaskDataOrPanic(nil), Dependencies: deps, Cell: "test", CellRepo: "example.test/test"})
				require.NoError(t, err)
				return h
			}
			get := func(h jobdb.JobHandle) jobdb.GoalInfo {
				t.Helper()
				g, err := recipejob.GetGoal(ctx, engine, jobdb.GoalKey{TenantId: "tenant", GoalID: h.GoalID})
				require.NoError(t, err)
				return g
			}
			complete := func(h jobdb.JobHandle) error {
				worker := wf.ExecutionWorker{JobType: "candidate", Execute: func(_ wf.ExecutionContext, in wf.JobData) (wf.JobData, error) { return in, nil }}
				r, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: worker, WorkerID: h.JobKey.JobId, LeaseDuration: time.Minute})
				if err != nil {
					return err
				}
				_, err = r.Run(nil)
				return err
			}
			a := submit("a", "")
			require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: a.JobKey}))
			cancelled, err := rt.GetJob(ctx, a.JobKey)
			require.NoError(t, err)
			require.Equal(t, jobdb.JobStatusCancelled, cancelled.Status)
			require.Equal(t, jobdb.GoalOpen, get(a).State, "cancelling an attempt must not fail its goal")
			b := submit("b", a.GoalID)
			_, err = rt.(jobdb.JobDeletionRuntime).DeleteJobs(ctx, jobdb.DeleteJobsRequest{TenantID: "tenant", JobIDs: []string{a.JobKey.JobId}})
			require.NoError(t, err)
			var ids []string
			token := ""
			for {
				page, err := recipejob.ListGoalCandidates(ctx, engine, recipejob.GoalCandidatesRequest{TenantID: "tenant", GoalID: a.GoalID, PageSize: 1, PageToken: token})
				require.NoError(t, err)
				for _, j := range page.Jobs {
					ids = append(ids, j.JobID)
				}
				if page.NextPageToken == "" {
					break
				}
				require.NotEqual(t, token, page.NextPageToken)
				token = page.NextPageToken
			}
			require.ElementsMatch(t, []string{"a", "b"}, ids)
			other, err := recipejob.ListGoalCandidates(ctx, engine, recipejob.GoalCandidatesRequest{TenantID: "other", GoalID: a.GoalID})
			require.NoError(t, err)
			require.Empty(t, other.Jobs)
			consumer := submit("consumer", "", jobdb.Dependency{Target: jobdb.DependencyTarget{GoalID: a.GoalID}, Condition: jobdb.JobPrereqSuccess})
			failReq := jobdb.FailGoalRequest{GoalKey: get(a).GoalKey, Message: "  attempts exhausted café\n"}
			failed, err := recipejob.FailGoal(ctx, engine, failReq)
			require.NoError(t, err)
			retry, err := recipejob.FailGoal(ctx, engine, failReq)
			require.NoError(t, err)
			require.Equal(t, failed, retry)
			require.Equal(t, failReq.Message, failed.Failure.Message)
			require.ElementsMatch(t, []string{"a"}, failed.Failure.TerminalJobIDs)
			require.ElementsMatch(t, []string{"b"}, failed.Failure.NonTerminalJobIDs)
			active, err := rt.GetJob(ctx, b.JobKey)
			require.NoError(t, err)
			require.Equal(t, jobdb.JobStatusReady, active.Status, "failing a goal must not cancel candidates")
			history, err := recipejob.ListDependencyWaits(ctx, engine, jobdb.ListDependencyWaitsRequest{TenantId: "tenant", ConsumerJobID: consumer.JobKey.JobId})
			require.NoError(t, err)
			require.Len(t, history.Waits, 1)
			require.Equal(t, jobdb.DependencyFailed, history.Waits[0].State)
			require.Equal(t, failed.Failure, history.Waits[0].Failure)
			require.NoError(t, complete(b))
			require.Equal(t, failed, get(a), "late success must not reopen an explicitly failed goal")
			require.Equal(t, failed, get(b), "late success must retain the shared failed goal")
			_, err = recipejob.FailGoal(ctx, engine, jobdb.FailGoalRequest{GoalKey: get(a).GoalKey, Message: "different"})
			require.ErrorIs(t, err, jobdb.ErrGoalConflict)
			c := submit("c", "")
			require.NoError(t, complete(c))
			_, err = recipejob.FailGoal(ctx, engine, jobdb.FailGoalRequest{GoalKey: get(c).GoalKey, Message: "too late"})
			require.ErrorIs(t, err, jobdb.ErrGoalConflict)
			_, err = recipejob.GetGoal(ctx, engine, jobdb.GoalKey{TenantId: "other", GoalID: a.GoalID})
			require.ErrorIs(t, err, jobdb.ErrGoalNotFound)
			_, err = recipejob.FailGoal(ctx, engine, jobdb.FailGoalRequest{GoalKey: jobdb.GoalKey{TenantId: "other", GoalID: a.GoalID}, Message: "wrong tenant"})
			require.Error(t, err)
		})
	}
}

// Administrator calls use the trusted endpoint; restricted task credentials must
// be rejected by the backend before reaching goal lifecycle operations.
func TestGoalFailureRejectsSubmitOnlyHTTPAuthority(t *testing.T) {
	ctx := celltest.Context(context.Background())
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
	server := httptest.NewServer(celltest.Handler(remote.NewServer(db)))
	t.Cleanup(server.Close)
	client, err := remote.New(server.URL, server.Client())
	require.NoError(t, err)
	celltest.Create(t, db, "tenant", "test", "example.test/test")
	parent, err := client.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "parent", JobType: "candidate", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	owner, err := client.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: parent.JobKey, WorkerID: "test", Routes: []jobdb.Route{{JobType: "candidate"}}, LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.NotNil(t, owner)
	task, err := client.CreateDelegation(ctx, owner, jobdb.CreateDelegationRequest{Permissions: jobdb.DelegationPermissions{Submit: &jobdb.DelegationSubmitPermission{JobTypes: jobdb.DelegationNames{Any: true}, JobIDs: jobdb.DelegationNames{Any: true}}}})
	require.NoError(t, err)
	// Establish that this is a live credential with working submission authority.
	_, err = task.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "child", JobType: "candidate", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	for _, tenant := range []string{"tenant", "other"} {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/tenants/"+tenant+"/goals/"+parent.GoalID+"/fail", strings.NewReader(`{"message":"unauthorized"}`))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-JobDB-Lease-Token", task.(interface{ LeaseToken() string }).LeaseToken())
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusForbidden, response.StatusCode, string(body))
		require.Contains(t, string(body), "lease_permission_denied")
	}
	engine := goalEngine(t, client)
	key := jobdb.GoalKey{TenantId: "tenant", GoalID: parent.GoalID}
	before, err := recipejob.GetGoal(ctx, engine, key)
	require.NoError(t, err)
	require.Equal(t, jobdb.GoalOpen, before.State)
	failed, err := recipejob.FailGoal(ctx, engine, jobdb.FailGoalRequest{GoalKey: key, Message: "authorized"})
	require.NoError(t, err)
	require.Equal(t, jobdb.GoalFailed, failed.State)
}
