package recipejob

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type executionLister struct {
	calls int
}

func (l *executionLister) ListJobs(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	l.calls++
	return jobdb.ListJobsResponse{}, nil
}
func summaryWithMemory(t *testing.T, id, memory string) jobdb.JobSummary {
	d, err := execution.Initial(nil, true, execution.Requirements{Resources: execution.Resources{Memory: &memory}})
	require.NoError(t, err)
	meta, err := json.Marshal(map[string]any{"v": 1, "recipe": "test", "execution": d})
	require.NoError(t, err)
	return jobdb.JobSummary{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: id}, Status: jobdb.JobStatusReady, JobType: "recipe", Metadata: meta}
}
func TestExecutionFilteringRequiresExplicitInspection(t *testing.T) {
	l := &executionLister{}
	_, err := ListExecutionJobs(context.Background(), l, jobdb.ListJobsRequest{}, &execution.Filter{IncludeUnresolved: true})
	require.ErrorIs(t, err, joblist.ErrExecutionDetailsRequired)
	require.Zero(t, l.calls)
}

func TestExecutionVisibilityAndFilteringOnlyForWaitingJobs(t *testing.T) {
	job := summaryWithMemory(t, "job", "2Gi")
	memory := "16Gi"
	f := execution.Filter{Allocation: execution.Allocation{SchemaVersion: 1, Resources: execution.Resources{Memory: &memory}}, IncludeUnresolved: true}
	for _, status := range []jobdb.JobStatus{jobdb.JobStatusReady, jobdb.JobStatusPendingJobs, jobdb.JobStatusAwaitingFuture, jobdb.JobStatusExpired, jobdb.JobStatusCrashConcern} {
		job.Status = status
		view := ExecutionView(job)
		require.Equal(t, "not_loaded", view.Status)
		require.Nil(t, view.Demand)
		require.NotNil(t, view.Initial)
		match, err := f.Match(view)
		require.ErrorContains(t, err, "not loaded")
		require.False(t, match)
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
}
