package joblist

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/colony-2/c2j/internal/repository"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Lister is the read-only backend used by the shared pagination helper.
type Lister interface {
	ListJobs(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error)
}

// Query selects jobs within the client's tenant and one repository.
type Query struct {
	// Repository is an explicit remote repository URL, SCP-style Git remote,
	// or host/owner/repository identity. Configured short names and local paths
	// are not resolved by this library. An absolute file URL can identify jobs
	// previously submitted from a local repository, without accessing that path.
	Repository string
	// Empty JobTypes includes every job type in the selected repository.
	JobTypes []string
	// Empty Statuses selects DefaultVisibleStatuses, matching c2j list.
	Statuses      []jobdb.JobStatus
	JobIDs        []string
	JobTasks      []jobdb.JobTaskFilter
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	PageSize      int
	PageToken     string
	// ExecutionFilter optionally fills a logical page by scanning backend pages.
	// Invalid demands fail the page in this mode; otherwise they are item diagnostics.
	ExecutionFilter *execution.Filter
}

// BuildRequest owns repository filtering, validation, and CLI-compatible query
// defaults. Most callers should use Client.List instead of assembling a backend.
func BuildRequest(tenantID string, query Query) (jobdb.ListJobsRequest, error) {
	invalid := func(err error) (jobdb.ListJobsRequest, error) {
		return jobdb.ListJobsRequest{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return invalid(fmt.Errorf("tenant ID is required"))
	}
	repo, err := repository.NormalizeIdentity(query.Repository)
	if err != nil {
		return invalid(err)
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
		allocation, err := query.ExecutionFilter.Allocation.Normalize()
		if err != nil {
			return invalid(err)
		}
		if !allocation.HasCompatibilityFacts() {
			return invalid(fmt.Errorf("execution filter requires an allocation fact"))
		}
	}
	metadata, err := jobdb.Metadata().EqualFilter("repo", repo)
	if err != nil {
		return invalid(err)
	}
	keys := make([]jobdb.JobKey, 0, len(query.JobIDs))
	for _, id := range query.JobIDs {
		if id = strings.TrimSpace(id); id != "" {
			keys = append(keys, jobdb.JobKey{TenantId: tenantID, JobId: id})
		}
	}
	req := jobdb.ListJobsRequest{
		TenantIds: []string{tenantID}, Statuses: statuses, Stores: StoresForStatuses(statuses),
		JobTypes: append([]string(nil), query.JobTypes...), JobKeys: keys,
		JobTasks: append([]jobdb.JobTaskFilter(nil), query.JobTasks...), MetadataFilter: metadata,
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
