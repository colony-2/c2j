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

func TestFilteredEmptyPagesAndNonAdvancingCursor(t *testing.T) {
	memory := "4Gi"
	filter := &execution.Filter{Allocation: execution.Allocation{SchemaVersion: 1, Resources: execution.Resources{Memory: &memory}}}
	item := demandJob(t, "matching", "2Gi")
	calls := 0
	lister := listerFunc(func(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
		calls++
		if req.PageToken == "" {
			return jobdb.ListJobsResponse{NextPageToken: "second"}, nil
		}
		return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{item}}, nil
	})
	page, err := joblist.ListExecutionJobs(context.Background(), lister, jobdb.ListJobsRequest{PageSize: 1}, filter)
	require.NoError(t, err)
	require.Len(t, page.Jobs, 1)
	require.Equal(t, 2, calls)
	stuck := listerFunc(func(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
		return jobdb.ListJobsResponse{NextPageToken: "same"}, nil
	})
	_, err = joblist.ListExecutionJobs(context.Background(), stuck, jobdb.ListJobsRequest{PageToken: "same", PageSize: 1}, filter)
	require.ErrorContains(t, err, "cursor did not advance")
}
