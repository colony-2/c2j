package recipejob

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// CellGoalDiscovery composes the existing read ports without lifecycle authority.
type CellGoalDiscovery interface {
	GoalDiscovery
	Lister
}

// ListGoalsInCells uses first-class candidate ownership. JobDB's current goal
// discovery request has no cell selector; the candidate job IDs retain its
// native pagination and whole-goal counts without trusting application metadata.
func ListGoalsInCells(ctx context.Context, rt CellGoalDiscovery, req jobdb.ListGoalsRequest, cells []string) (jobdb.ListGoalsResponse, error) {
	return jobdb.ListGoalsResponse{}, fmt.Errorf("goal queries are unsupported; select an exact job: %w", errors.ErrUnsupported)
}

// JobIDsInCells resolves ownership using JobDB's first-class selector before administrative filtering.
func JobIDsInCells(ctx context.Context, rt Lister, tenant string, cells, requested []string) ([]string, error) {
	jobs := jobdb.ListJobsRequest{TenantIds: []string{tenant}, CellNames: cells, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}, IncludeHidden: true, PageSize: jobdb.MaxListJobsPageSize}
	var ids []string
	for {
		page, err := rt.ListJobs(ctx, jobs)
		if err != nil {
			return nil, err
		}
		for _, job := range page.Jobs {
			if len(requested) == 0 || slices.Contains(requested, job.JobKey.JobId) {
				ids = append(ids, job.JobKey.JobId)
			}
		}
		if page.NextPageToken == "" {
			break
		}
		jobs.PageToken = page.NextPageToken
	}
	return ids, nil
}
