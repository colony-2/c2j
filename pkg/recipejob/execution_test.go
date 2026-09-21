package recipejob

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"strconv"
	"testing"
)

type pagedExecutionLister struct {
	jobs  []jobdb.JobSummary
	calls int
}

func (l *pagedExecutionLister) ListJobs(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	l.calls++
	start, _ := strconv.Atoi(req.PageToken)
	end := start + req.PageSize
	if end > len(l.jobs) {
		end = len(l.jobs)
	}
	resp := jobdb.ListJobsResponse{Jobs: l.jobs[start:end]}
	if end < len(l.jobs) {
		resp.NextPageToken = strconv.Itoa(end)
	}
	return resp, nil
}
func summaryWithMemory(t *testing.T, id, memory string) jobdb.JobSummary {
	d, err := execution.Initial(nil, "pinned", execution.Requirements{Resources: execution.Resources{Memory: &memory}})
	require.NoError(t, err)
	meta, err := json.Marshal(map[string]any{"v": 1, "recipe": "test", "execution": d})
	require.NoError(t, err)
	return jobdb.JobSummary{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: id}, Status: jobdb.JobStatusReady, JobType: "recipe", Metadata: meta}
}
func TestExecutionFilteringFillsPagesWithoutSkippingMatches(t *testing.T) {
	l := &pagedExecutionLister{jobs: []jobdb.JobSummary{summaryWithMemory(t, "1", "8Gi"), summaryWithMemory(t, "2", "2Gi"), summaryWithMemory(t, "3", "8Gi"), summaryWithMemory(t, "4", "2Gi"), summaryWithMemory(t, "5", "2Gi")}}
	memory := "4Gi"
	filter := &execution.Filter{Allocation: execution.Allocation{SchemaVersion: 1, Resources: execution.Resources{Memory: &memory}}}
	req := jobdb.ListJobsRequest{PageSize: 2}
	first, err := ListExecutionJobs(context.Background(), l, req, filter)
	require.NoError(t, err)
	require.Len(t, first.Jobs, 2)
	require.Equal(t, "2", first.Jobs[0].JobKey.JobId)
	require.Equal(t, "4", first.Jobs[1].JobKey.JobId)
	require.Greater(t, l.calls, 1)
	req.PageToken = first.NextPageToken
	second, err := ListExecutionJobs(context.Background(), l, req, filter)
	require.NoError(t, err)
	require.Len(t, second.Jobs, 1)
	require.Equal(t, "5", second.Jobs[0].JobKey.JobId)
	require.Empty(t, second.NextPageToken)
	l.jobs = []jobdb.JobSummary{{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "bad"}, Status: jobdb.JobStatusReady, ClientPayload: json.RawMessage(`{"c2j":{"execution":{"schema_version":99}}}`)}}
	req.PageToken = ""
	_, err = ListExecutionJobs(context.Background(), l, req, filter)
	var demandErr *ExecutionDemandError
	require.ErrorAs(t, err, &demandErr)
	require.Equal(t, "bad", demandErr.JobKey.JobId)
}

func TestExecutionVisibilityAndFilteringOnlyForWaitingJobs(t *testing.T) {
	job := summaryWithMemory(t, "job", "2Gi")
	memory := "16Gi"
	f := execution.Filter{Allocation: execution.Allocation{SchemaVersion: 1, Resources: execution.Resources{Memory: &memory}}, IncludeUnresolved: true}
	for _, status := range []jobdb.JobStatus{jobdb.JobStatusReady, jobdb.JobStatusPendingJobs, jobdb.JobStatusAwaitingFuture, jobdb.JobStatusExpired, jobdb.JobStatusCrashConcern} {
		job.Status = status
		view := ExecutionView(job)
		require.Equal(t, "specified", view.Status)
		require.NotNil(t, view.Demand)
		match, err := f.Match(view)
		require.NoError(t, err)
		require.True(t, match)
	}
	for _, status := range []jobdb.JobStatus{jobdb.JobStatusActive, jobdb.JobStatusCompleted, jobdb.JobStatusCancelled} {
		job.Status = status
		view := ExecutionView(job)
		require.Nil(t, view.Demand)
		require.Nil(t, view.Initial)
		require.NotEqual(t, "unspecified", view.Status)
		match, err := f.Match(view)
		require.NoError(t, err)
		require.False(t, match, "include-unresolved must not admit active/terminal jobs")
		dto, ok, err := RecipeJobFromSummary(job)
		require.NoError(t, err)
		require.True(t, ok)
		require.Nil(t, dto.Execution.Demand)
	}
	active := job
	active.Status = jobdb.JobStatusActive
	waiting := summaryWithMemory(t, "waiting", "2Gi")
	l := &pagedExecutionLister{jobs: []jobdb.JobSummary{active, waiting}}
	page, err := ListExecutionJobs(context.Background(), l, jobdb.ListJobsRequest{PageSize: 1}, &f)
	require.NoError(t, err)
	require.Len(t, page.Jobs, 1)
	require.Equal(t, "waiting", page.Jobs[0].JobKey.JobId)
	require.Equal(t, 2, l.calls)
}
