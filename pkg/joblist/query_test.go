package joblist_test

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestQueryDefaultsAndOwnership(t *testing.T) {
	q := joblist.Query{Repository: "git@example.com:acme/app.git"}
	req, err := joblist.BuildRequest("tenant", q)
	require.NoError(t, err)
	require.Equal(t, joblist.DefaultVisibleStatuses(), req.Statuses)
	require.Equal(t, []jobdb.JobStore{jobdb.JobStoreActive}, req.Stores)
	require.Empty(t, req.JobTypes, "not restricted to recipe jobs by default")
	predicates, err := jobdb.MetadataPredicates(req.MetadataFilter)
	require.NoError(t, err)
	require.Equal(t, []any{"ssh://git@example.com/acme/app.git"}, predicates[0].Values)
	start, end := time.Unix(10, 0).UTC(), time.Unix(20, 0).UTC()
	q.Statuses = []jobdb.JobStatus{jobdb.JobStatusReady, jobdb.JobStatusCompleted}
	q.JobTypes = []string{"custom:job"}
	q.JobTasks = []jobdb.JobTaskFilter{{JobType: "custom:job", TaskType: "step:next"}}
	q.JobIDs = []string{" job-id "}
	q.CreatedAfter, q.CreatedBefore = &start, &end
	req, err = joblist.BuildRequest("tenant", q)
	require.NoError(t, err)
	require.Equal(t, []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}, req.Stores)
	require.Equal(t, []jobdb.JobKey{{TenantId: "tenant", JobId: "job-id"}}, req.JobKeys)
	q.Statuses[0], q.JobTypes[0], q.JobTasks[0].TaskType = jobdb.JobStatusActive, "changed", "changed"
	start = start.Add(time.Hour)
	require.Equal(t, jobdb.JobStatusReady, req.Statuses[0])
	require.Equal(t, "custom:job", req.JobTypes[0])
	require.Equal(t, "step:next", req.JobTasks[0].TaskType)
	require.Equal(t, time.Unix(10, 0).UTC(), *req.CreatedAfter)
	_, err = joblist.BuildRequest("tenant", q)
	require.ErrorIs(t, err, joblist.ErrInvalidInput, "reversed date range")
	_, err = joblist.BuildRequest("", joblist.Query{Repository: "github.com/acme/app"})
	require.ErrorIs(t, err, joblist.ErrInvalidInput)
	var c joblist.Client
	_, err = c.List(context.Background(), q)
	require.ErrorIs(t, err, joblist.ErrInvalidInput)
}

type listerFunc func(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error)

func (f listerFunc) ListJobs(ctx context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	return f(ctx, req)
}

func TestCompactListingPassesEmptyPageThrough(t *testing.T) {
	calls := 0
	lister := listerFunc(func(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
		calls++
		require.Equal(t, 1, req.PageSize)
		return jobdb.ListJobsResponse{NextPageToken: "second"}, nil
	})
	page, err := joblist.ListExecutionJobs(context.Background(), lister, jobdb.ListJobsRequest{PageSize: 1}, nil)
	require.NoError(t, err)
	require.Empty(t, page.Jobs)
	require.Equal(t, "second", page.NextPageToken)
	require.Equal(t, 1, calls)
	_, err = joblist.ListExecutionJobs(context.Background(), lister, jobdb.ListJobsRequest{}, &execution.Filter{IncludeUnresolved: true})
	require.ErrorIs(t, err, joblist.ErrExecutionDetailsRequired)
	require.Equal(t, 1, calls)
}

func TestIncludeHiddenRequestAndValidation(t *testing.T) {
	for _, include := range []bool{false, true} {
		q := joblist.Query{Repository: "github.com/acme/app", IncludeHidden: include, GoalIDs: []string{"goal"}, JobIDs: []string{"job"}, JobTasks: []jobdb.JobTaskFilter{{JobType: "recipe", TaskType: "step"}}, PageSize: 2, PageToken: " token "}
		req, err := joblist.BuildRequest("tenant", q)
		require.NoError(t, err)
		require.Equal(t, include, req.IncludeHidden)
		require.Equal(t, []string{"goal"}, req.GoalIDs)
		require.Equal(t, q.JobTasks, req.JobTasks)
		require.Equal(t, 2, req.PageSize)
		require.Equal(t, "token", req.PageToken)
		require.Equal(t, joblist.DefaultVisibleStatuses(), req.Statuses)
	}
	calls := 0
	s := serve(t, func(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
		calls++
		return jobdb.ListJobsResponse{}, nil
	})
	c := client(t, s, "tenant")
	later, earlier := time.Unix(20, 0), time.Unix(10, 0)
	for _, q := range []joblist.Query{
		{Repository: "", IncludeHidden: true},
		{Repository: "github.com/acme/app", IncludeHidden: true, PageSize: -1},
		{Repository: "github.com/acme/app", IncludeHidden: true, Statuses: []jobdb.JobStatus{"invalid"}},
		{Repository: "github.com/acme/app", IncludeHidden: true, CreatedAfter: &later, CreatedBefore: &earlier},
	} {
		_, err := c.List(context.Background(), q)
		require.ErrorIs(t, err, joblist.ErrInvalidInput)
	}
	_, err := joblist.BuildRequest("", joblist.Query{Repository: "github.com/acme/app", IncludeHidden: true})
	require.ErrorIs(t, err, joblist.ErrInvalidInput)
	require.Zero(t, calls)
}
