package joblist

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Lister is the read-only backend used by the shared pagination helper.
type Lister interface {
	ListJobs(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error)
}

// Query selects jobs within the client's tenant and selected cells.
type Query struct {
	// IncludeHidden includes mark-deleted jobs within the other filters.
	IncludeHidden bool
	GoalIDs       []string
	CellNames     []string
	// Empty JobTypes includes every job type in the selected cells.
	JobTypes []string
	// Empty Statuses selects DefaultVisibleStatuses, matching c2j list.
	Statuses      []jobdb.JobStatus
	JobIDs        []string
	JobTasks      []jobdb.JobTaskFilter
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	PageSize      int
	PageToken     string
	// Deprecated: compact lists cannot filter current execution demand.
	// A non-nil filter returns ErrExecutionDetailsRequired.
	ExecutionFilter *execution.Filter
}

// BuildRequest owns cell filtering, validation, and CLI-compatible query
// defaults. Most callers should use Client.List instead of assembling a backend.
func BuildRequest(tenantID string, query Query) (jobdb.ListJobsRequest, error) {
	invalid := func(err error) (jobdb.ListJobsRequest, error) {
		return jobdb.ListJobsRequest{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return invalid(fmt.Errorf("tenant ID is required"))
	}
	if query.CellNames != nil && len(query.CellNames) == 0 {
		return invalid(fmt.Errorf("cell selector must not be empty"))
	}
	for _, name := range query.CellNames {
		if err := jobdb.ValidateCellName(name); err != nil {
			return invalid(err)
		}
	}
	if query.PageSize < 0 {
		return invalid(fmt.Errorf("page size must not be negative"))
	}
	if query.CreatedAfter != nil && query.CreatedBefore != nil && query.CreatedAfter.After(*query.CreatedBefore) {
		return invalid(fmt.Errorf("created-after must be <= created-before"))
	}
	statuses := append([]jobdb.JobStatus(nil), query.Statuses...)
	if len(statuses) == 0 {
		statuses = DefaultVisibleStatuses()
	}
	for _, status := range statuses {
		switch status {
		case jobdb.JobStatusReady, jobdb.JobStatusExpired, jobdb.JobStatusPendingJobs,
			jobdb.JobStatusAwaitingFuture, jobdb.JobStatusActive, jobdb.JobStatusCrashConcern,
			jobdb.JobStatusCancelled, jobdb.JobStatusCompleted:
		default:
			return invalid(fmt.Errorf("unsupported status %q", status))
		}
	}
	for _, kind := range query.JobTypes {
		if err := jobdb.ValidateIdentifier(kind); err != nil {
			return invalid(fmt.Errorf("job type: %w", err))
		}
	}
	if query.ExecutionFilter != nil {
		return jobdb.ListJobsRequest{}, fmt.Errorf("%w: %w", ErrInvalidInput, ErrExecutionDetailsRequired)
	}
	keys := make([]jobdb.JobKey, 0, len(query.JobIDs))
	for _, id := range query.JobIDs {
		if id = strings.TrimSpace(id); id != "" {
			keys = append(keys, jobdb.JobKey{TenantId: tenantID, JobId: id})
		}
	}
	req := jobdb.ListJobsRequest{
		IncludeHidden: query.IncludeHidden,
		TenantIds:     []string{tenantID}, Statuses: statuses, Stores: StoresForStatuses(statuses),
		JobTypes: append([]string(nil), query.JobTypes...), GoalIDs: append([]string(nil), query.GoalIDs...),
		JobKeys:  keys,
		JobTasks: append([]jobdb.JobTaskFilter(nil), query.JobTasks...), CellNames: slices.Clone(query.CellNames),
		CreatedAfter: cloneTime(query.CreatedAfter), CreatedBefore: cloneTime(query.CreatedBefore),
		PageSize: query.PageSize, PageToken: strings.TrimSpace(query.PageToken),
	}
	if err := req.ValidateRoutes(); err != nil {
		return invalid(err)
	}
	return req, nil
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
