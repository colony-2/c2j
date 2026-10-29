package recipejob

import (
	"context"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// ExecutionDemandError identifies a job with unusable execution requirements.
type ExecutionDemandError = joblist.ExecutionDemandError

// ExecutionView marks current requirements as not_loaded on compact waiting rows.
func ExecutionView(job jobdb.JobSummary) execution.View {
	return joblist.ExecutionView(job)
}

// ListExecutionJobs returns one compact backend page; compatibility filters require details.
func ListExecutionJobs(ctx context.Context, lister Lister, req jobdb.ListJobsRequest, filter *execution.Filter) (jobdb.ListJobsResponse, error) {
	return joblist.ListExecutionJobs(ctx, lister, req, filter)
}

type workflowListAdapter struct{ workflowLister }

func (a workflowListAdapter) ListJobs(ctx context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	items, next, err := a.workflowLister.ListJobs(ctx, req)
	resp := jobdb.ListJobsResponse{NextPageToken: next}
	for _, item := range items {
		resp.Jobs = append(resp.Jobs, item.JobSummary)
	}
	return resp, err
}
