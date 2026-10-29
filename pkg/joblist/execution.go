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

// ErrExecutionDetailsRequired rejects compatibility filtering until its
// pagination contract can preserve one backend page per list page.
var ErrExecutionDetailsRequired = errors.New("execution compatibility requires job details; inspect selected jobs or use the acquired lease payload")

// ExecutionView interprets the restored list projection. An absent payload uses
// the same submission/absence semantics as an explicit job read.
func ExecutionView(job jobdb.JobSummary) execution.View {
	return ExecutionViewFromInfo(job.Metadata, jobdb.JobInfo{Status: job.Status, ClientPayload: job.ClientPayload})
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
// Compatibility filters remain rejected: restoring stored payloads does not
// define filtered pagination without extra backend scans.
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
