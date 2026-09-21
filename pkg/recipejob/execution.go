package recipejob

import (
	"context"
	"fmt"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type ExecutionDemandError struct {
	JobKey jobdb.JobKey
	Err    error
}

func (e *ExecutionDemandError) Error() string { return fmt.Sprintf("job %s: %v", e.JobKey, e.Err) }
func (e *ExecutionDemandError) Unwrap() error { return e.Err }

// ExecutionView exposes scheduling requirements only while a job is waiting.
// Active jobs can change lexical needs without publishing a scheduler snapshot.
func ExecutionView(job jobdb.JobSummary) execution.View {
	switch job.Status {
	case jobdb.JobStatusReady, jobdb.JobStatusPendingJobs, jobdb.JobStatusAwaitingFuture,
		jobdb.JobStatusExpired, jobdb.JobStatusCrashConcern:
		return execution.Inspect(job.Metadata, job.ClientPayload)
	case jobdb.JobStatusActive:
		return execution.View{Status: "in_flight", Source: "unavailable"}
	default:
		return execution.View{Status: "not_waiting", Source: "unavailable"}
	}
}

// ListExecutionJobs fills a logical page with matching jobs, scanning further
// underlying pages as needed. Each fetch is bounded by the remaining capacity,
// so the backend cursor always resumes after the last examined job without a
// client-side buffer or skipped matches. Keep the same filters on later calls.
func ListExecutionJobs(ctx context.Context, lister Lister, req jobdb.ListJobsRequest, filter *execution.Filter) (jobdb.ListJobsResponse, error) {
	if filter == nil {
		return lister.ListJobs(ctx, req)
	}
	allocation, err := filter.Allocation.Normalize()
	if err != nil {
		return jobdb.ListJobsResponse{}, err
	}
	if !allocation.HasCompatibilityFacts() {
		return jobdb.ListJobsResponse{}, fmt.Errorf("execution filter requires an allocation fact")
	}
	f := *filter
	f.Allocation = allocation
	size := req.PageSize
	if size == 0 {
		size = 100
	}
	if size < 0 {
		return jobdb.ListJobsResponse{}, fmt.Errorf("page size must not be negative")
	}
	result := jobdb.ListJobsResponse{}
	for {
		req.PageSize = size - len(result.Jobs)
		page, err := lister.ListJobs(ctx, req)
		if err != nil {
			return jobdb.ListJobsResponse{}, err
		}
		for _, job := range page.Jobs {
			match, err := f.Match(ExecutionView(job))
			if err != nil {
				return jobdb.ListJobsResponse{}, &ExecutionDemandError{JobKey: job.JobKey, Err: err}
			}
			if match {
				result.Jobs = append(result.Jobs, job)
			}
		}
		result.NextPageToken = page.NextPageToken
		if page.NextPageToken == "" || len(result.Jobs) >= size {
			return result, nil
		}
		if page.NextPageToken == req.PageToken {
			return jobdb.ListJobsResponse{}, fmt.Errorf("job listing cursor did not advance")
		}
		req.PageToken = page.NextPageToken
	}
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
