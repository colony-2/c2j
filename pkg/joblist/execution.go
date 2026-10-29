package joblist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// ExecutionDemandError identifies a job with unusable demand.
// Deprecated: compact list filtering returns ErrExecutionDetailsRequired.
type ExecutionDemandError struct {
	JobKey jobdb.JobKey
	Err    error
}

func (e *ExecutionDemandError) Error() string { return fmt.Sprintf("job %s: %v", e.JobKey, e.Err) }
func (e *ExecutionDemandError) Unwrap() error { return e.Err }

// ErrExecutionDetailsRequired prevents compact discovery from silently using
// stale submission requirements or fetching every listed job's client payload.
var ErrExecutionDetailsRequired = errors.New("execution compatibility requires job details; inspect selected jobs or use the acquired lease payload")

// ExecutionView never treats an omitted list payload as an absent demand.
// Initial is an immutable submission hint, not current scheduling requirements.
func ExecutionView(job jobdb.JobSummary) execution.View {
	v := ExecutionViewFromInfo(job.Metadata, jobdb.JobInfo{Status: job.Status})
	if v.Status != "in_flight" && v.Status != "not_waiting" {
		v.Status, v.Source, v.Demand, v.Published = "not_loaded", "unavailable", nil, false
	}
	return v
}

// ExecutionViewFromInfo interprets explicitly loaded job details. A lease holder
// should use execution.Inspect with the lease payload instead.
func ExecutionViewFromInfo(metadata json.RawMessage, job jobdb.JobInfo) execution.View {
	switch job.Status {
	case jobdb.JobStatusReady, jobdb.JobStatusPendingJobs, jobdb.JobStatusAwaitingFuture,
		jobdb.JobStatusExpired, jobdb.JobStatusCrashConcern:
		return execution.Inspect(metadata, job.ClientPayload)
	case jobdb.JobStatusActive:
		return execution.View{Status: "in_flight", Source: "unavailable"}
	default:
		return execution.View{Status: "not_waiting", Source: "unavailable"}
	}
}

// ListExecutionJobs returns exactly one backend page without enrichment.
// Deprecated compatibility filters are rejected because lists omit demand.
func ListExecutionJobs(ctx context.Context, lister Lister, req jobdb.ListJobsRequest, filter *execution.Filter) (jobdb.ListJobsResponse, error) {
	if ctx == nil || lister == nil {
		return jobdb.ListJobsResponse{}, fmt.Errorf("%w: context and job lister are required", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return jobdb.ListJobsResponse{}, err
	}
	if filter != nil {
		return jobdb.ListJobsResponse{}, ErrExecutionDetailsRequired
	}
	page, err := lister.ListJobs(ctx, req)
	if err != nil {
		return jobdb.ListJobsResponse{}, err
	}
	jobs := make([]jobdb.JobSummary, len(page.Jobs))
	for i, job := range page.Jobs {
		jobs[i] = CompactSummary(job)
	}
	page.Jobs = jobs
	return page, nil
}
