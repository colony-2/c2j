package joblist_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

func TestIncludeHiddenSQLiteHTTP(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "jobs.db")
	rt, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rt.Close(ctx)) })
	created := time.Now().UTC().Add(-time.Hour)
	for _, row := range []struct{ tenant, id, repo string }{
		{"tenant", "a", "app"}, {"tenant", "b", "app"}, {"tenant", "c", "app"},
		{"tenant", "wrong-repo", "other"}, {"other", "wrong-tenant", "app"},
	} {
		meta, err := json.Marshal(map[string]string{"repo": "https://github.com/acme/" + row.repo + ".git", "title": "stored title"})
		require.NoError(t, err)
		_, err = rt.SubmitJob(ctx, jobdb.SubmitJobRequest{RequestTime: created, Job: jobdb.SubmitJob{TenantId: row.tenant, JobID: row.id, JobType: "recipe", Metadata: meta, Data: jobdb.NewTaskDataOrPanic("input")}})
		require.NoError(t, err)
	}
	// Normalize persisted fixture timestamps: SQLite submissions use wall-clock creation,
	// not RequestTime. Equal timestamps exercise the cursor's ID tie-breaker.
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "UPDATE jobdb_jobs SET created_at_ns = ?", created.UnixNano())
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = rt.DeleteJobs(ctx, jobdb.DeleteJobsRequest{TenantID: "tenant", JobIDs: []string{"b"}})
	require.NoError(t, err)
	calls := 0
	s := serve(t, func(ctx context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
		calls++
		return rt.ListJobs(ctx, req)
	})
	c := client(t, s, "tenant")
	traverse := func(q joblist.Query) []string {
		t.Helper()
		var ids []string
		for n := 0; n < 10; n++ {
			before := calls
			page, err := c.List(ctx, q)
			require.NoError(t, err)
			require.Equal(t, before+1, calls)
			require.NotNil(t, page.Jobs)
			for _, j := range page.Jobs {
				ids = append(ids, j.JobID)
				require.Equal(t, "stored title", j.Title)
			}
			if page.NextPageToken == "" {
				return ids
			}
			require.NotEqual(t, q.PageToken, page.NextPageToken)
			q.PageToken = page.NextPageToken
		}
		t.Fatal("pagination did not terminate")
		return nil
	}
	q := joblist.Query{Repository: "github.com/acme/app", PageSize: 1, Statuses: []jobdb.JobStatus{jobdb.JobStatusReady, jobdb.JobStatusCancelled}}
	require.Equal(t, []string{"c", "a"}, traverse(q))
	q.IncludeHidden = true
	require.Equal(t, []string{"c", "b", "a"}, traverse(q))
	// Deletion archives/cancels b; inclusion must not widen default statuses.
	q.Statuses = nil
	require.Equal(t, []string{"c", "a"}, traverse(q))
	q.Statuses = []jobdb.JobStatus{jobdb.JobStatusCancelled}
	require.Equal(t, []string{"b"}, traverse(q))
	q.IncludeHidden = false
	require.Empty(t, traverse(q))
	q.IncludeHidden = true
	q.JobIDs = []string{"a"}
	require.Empty(t, traverse(q))
	q.Statuses = []jobdb.JobStatus{jobdb.JobStatusReady, jobdb.JobStatusCancelled}
	require.Equal(t, []string{"a"}, traverse(q))
	q.JobTypes = []string{"other"}
	require.Empty(t, traverse(q))
	q.JobTypes = nil
	q.JobTasks = []jobdb.JobTaskFilter{{JobType: "recipe", TaskType: "not-current"}}
	require.Empty(t, traverse(q))
	q.JobTasks = nil
	q.GoalIDs = []string{"missing-goal"}
	require.Empty(t, traverse(q))
	q.GoalIDs = nil
	before := created.Add(-time.Second)
	after := created.Add(time.Second)
	q.CreatedAfter = &after
	require.Empty(t, traverse(q))
	q.CreatedAfter = &before
	q.CreatedBefore = &after
	require.Equal(t, []string{"a"}, traverse(q))
	// Listing has not restored the hidden row or made it runnable.
	visible, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, JobKeys: []jobdb.JobKey{{TenantId: "tenant", JobId: "b"}}})
	require.NoError(t, err)
	require.Empty(t, visible.Jobs)
	lease, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "b"}, Routes: []jobdb.Route{{JobType: "recipe"}}, WorkerID: "test", LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.Nil(t, lease)
}
