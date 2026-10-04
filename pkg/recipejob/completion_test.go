package recipejob_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/c2j/pkg/recipejob"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// All non-list methods are nil: any history, artifact, recipe, or job read
// through this runtime fails the test instead of supplying fallback data.
type completionListOnly struct {
	jobdb.WorkflowRuntime
	list  func(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error)
	calls atomic.Int32
}

func (r *completionListOnly) ListJobs(ctx context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	r.calls.Add(1)
	return r.list(ctx, req)
}

type completionListEngine struct {
	jobworkflow.Engine
	lister recipejob.Lister
}

func (e completionListEngine) ListJobs(ctx context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	return e.lister.ListJobs(ctx, req)
}

func checkCompletionJSON(t *testing.T, value any, want jobdb.JobSummary) {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, string(want.Status), got["status"])
	for key, value := range map[string]string{"completion_status": want.CompletionStatus, "completion_detail": want.CompletionDetail} {
		if value == "" {
			require.NotContains(t, got, key)
		} else {
			require.Equal(t, value, got[key])
		}
	}
	if want.Status == jobdb.JobStatusActive {
		require.Equal(t, "in_flight", got["execution"].(map[string]any)["status"])
	}
	if want.Status == jobdb.JobStatusCompleted {
		require.Equal(t, "not_waiting", got["execution"].(map[string]any)["status"])
	}
}

func checkRecipeCompletionPaths(t *testing.T, lister recipejob.Lister, want jobdb.JobSummary, next string) {
	t.Helper()
	ctx := context.Background()
	projected, ok, err := recipejob.RecipeJobFromSummary(want)
	require.NoError(t, err)
	require.True(t, ok)
	checkCompletionJSON(t, projected, want)
	list, err := recipejob.ListRecipeJobs(ctx, lister, recipejob.ListRecipeJobsRequest{TenantID: want.JobKey.TenantId, JobIDs: []string{want.JobKey.JobId}, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}})
	require.NoError(t, err)
	require.Len(t, list.Jobs, 1)
	require.Equal(t, next, list.NextPageToken)
	checkCompletionJSON(t, list.Jobs[0], want)
	single, err := recipejob.GetRecipeJob(ctx, lister, recipejob.GetRecipeJobRequest{TenantID: want.JobKey.TenantId, JobID: want.JobKey.JobId})
	require.NoError(t, err)
	checkCompletionJSON(t, single, want)
	children := recipejob.ListChildRecipeJobsRequest{TenantID: want.JobKey.TenantId, ParentTenantID: want.JobKey.TenantId, ParentJobID: want.ParentJobID, AllParentInvocations: true, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}}
	list, err = recipejob.ListChildRecipeJobs(ctx, lister, children)
	require.NoError(t, err)
	require.Len(t, list.Jobs, 1)
	require.Equal(t, next, list.NextPageToken)
	checkCompletionJSON(t, list.Jobs[0], want)
	control := &workerworkflow.SWFWorkflowControl{Engine: completionListEngine{lister: lister}}
	list, err = recipejob.ListChildRecipeJobsFromWorkflow(ctx, control, children)
	require.NoError(t, err)
	require.Len(t, list.Jobs, 1)
	require.Equal(t, next, list.NextPageToken)
	checkCompletionJSON(t, list.Jobs[0], want)
	checkCompletionJSON(t, joblist.JobFromSummary(want), want)
}

func TestCompletionProjectionsUseOnlySummaryFields(t *testing.T) {
	for _, tc := range []struct {
		name, status, detail string
		scheduler            jobdb.JobStatus
		cancelRequested      bool
	}{
		{name: "success", status: "success"},
		{name: "application failure", status: "failed_app", detail: "  exact detail\nwith unicode: ☃\n"},
		{name: "system failure", status: "failed_system", detail: "container exited with status 1"},
		{name: "timeout", status: "failed_timeout", detail: "deadline exceeded"},
		{name: "cancelled", status: "cancelled", scheduler: jobdb.JobStatusCancelled},
		{name: "unknown category", status: "future_category", detail: "preserve"},
		{name: "empty failure detail", status: "failed_system"},
		{name: "old completed summary"},
		{name: "detail without status", detail: "legacy detail"},
		{name: "retrying after failure", scheduler: jobdb.JobStatusAwaitingFuture},
		{name: "cancel requested only", scheduler: jobdb.JobStatusActive, cancelRequested: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.scheduler == "" {
				tc.scheduler = jobdb.JobStatusCompleted
			}
			summary := jobdb.JobSummary{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, JobType: "recipe", ParentJobID: "parent", Status: tc.scheduler, CompletionStatus: tc.status, CompletionDetail: tc.detail, CancelRequested: tc.cancelRequested, Metadata: json.RawMessage(`{"recipe":"not-installed","repo":"https://github.com/acme/app.git"}`)}
			rt := &completionListOnly{list: func(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
				require.Equal(t, []string{"tenant"}, req.TenantIds)
				return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{summary}, NextPageToken: "next"}, nil
			}}
			checkRecipeCompletionPaths(t, rt, summary, "next")
			server := httptest.NewServer(remote.NewServer(rt))
			defer server.Close()
			client, err := remote.New(server.URL, server.Client())
			require.NoError(t, err)
			checkRecipeCompletionPaths(t, client, summary, "next")
			standalone, err := joblist.New(joblist.Config{JobDBURI: server.URL + "/tenant", HTTPClient: server.Client()})
			require.NoError(t, err)
			page, err := standalone.List(context.Background(), joblist.Query{Repository: "github.com/acme/app", Statuses: []jobdb.JobStatus{tc.scheduler}})
			require.NoError(t, err)
			require.Len(t, page.Jobs, 1)
			require.Equal(t, "next", page.NextPageToken)
			checkCompletionJSON(t, page.Jobs[0], summary)
			require.Equal(t, int32(9), rt.calls.Load(), "each projection should use one listing, without fallback reads")
		})
	}
}

func TestPersistedCompletionAcrossEmbeddedAndRemoteListings(t *testing.T) {
	for _, backend := range []string{"embedded", "remote"} {
		for _, status := range []string{"success", "failed_app", "failed_system", "failed_timeout", "cancelled", "retry"} {
			t.Run(backend+"/"+status, func(t *testing.T) {
				ctx := context.Background()
				db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
				var rt jobdb.WorkflowRuntime = db
				if backend == "remote" {
					server := httptest.NewServer(remote.NewServer(db))
					t.Cleanup(server.Close)
					rt, err = remote.New(server.URL, server.Client())
					require.NoError(t, err)
				}
				parent, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "parent", JobType: "parent", Data: jobdb.NewTaskDataOrPanic(map[string]any{})}})
				require.NoError(t, err)
				parentLease, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: parent.JobKey, WorkerID: "parent-test", Routes: []jobdb.Route{{JobType: "parent"}}})
				require.NoError(t, err)
				require.NotNil(t, parentLease)
				submitted, err := parentLease.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "child", JobType: "recipe", Metadata: json.RawMessage(`{"recipe":"not-installed"}`), Data: jobdb.NewTaskDataOrPanic(map[string]any{})}})
				require.NoError(t, err)
				lease, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: submitted.JobKey, WorkerID: "completion-test", Routes: []jobdb.Route{{JobType: "recipe"}}})
				require.NoError(t, err)
				require.NotNil(t, lease)
				chapter := jobdb.Chapter{Ordinal: 1, TaskType: "recipe", CreatedAt: time.Now().UTC(), Body: jobdb.JobAttemptOutcomeChapter{Outcome: jobdb.AppErrorOutcome{Error: jobdb.AppErrorPayload{Message: "earlier attempt failure"}}}}
				detail := "  persisted detail\n☃\n"
				if status == "success" {
					detail = ""
				}
				if status == "retry" {
					token := ""
					if bearer, ok := lease.(interface{ LeaseToken() string }); ok {
						token = bearer.LeaseToken()
					}
					require.NoError(t, rt.PutChapter(ctx, jobdb.PutChapterRequest{Ref: jobdb.ChapterRef{JobKey: submitted.JobKey, Ordinal: 1}, LeaseID: lease.LeaseID(), LeaseToken: token, Chapter: chapter}))
					require.NoError(t, lease.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}))
				} else {
					require.NoError(t, lease.Complete(ctx, jobdb.CompleteExecutionRequest{Status: status, Detail: detail, Chapter: &chapter}))
				}
				listed, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, JobKeys: []jobdb.JobKey{submitted.JobKey}, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}})
				require.NoError(t, err)
				require.Len(t, listed.Jobs, 1)
				got := listed.Jobs[0]
				if status == "retry" {
					require.Empty(t, got.CompletionStatus)
					require.Empty(t, got.CompletionDetail)
					require.Equal(t, jobdb.JobStatusReady, got.Status)
				} else {
					require.Equal(t, status, got.CompletionStatus)
					require.Equal(t, detail, got.CompletionDetail)
				}
				guarded := &completionListOnly{list: rt.ListJobs}
				checkRecipeCompletionPaths(t, guarded, got, "")
				require.Equal(t, int32(4), guarded.calls.Load())
			})
		}
	}
}
