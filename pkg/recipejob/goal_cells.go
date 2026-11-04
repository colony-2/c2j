package recipejob

import (
	"context"
	"slices"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// ListGoalsInCells uses first-class candidate ownership. JobDB's current goal
// discovery request has no cell selector; the candidate job IDs retain its
// native pagination and whole-goal counts without trusting application metadata.
func ListGoalsInCells(ctx context.Context, rt jobdb.WorkflowRuntime, req jobdb.ListGoalsRequest, cells []string) (jobdb.ListGoalsResponse, error) {
	if cells == nil {
		return ListGoals(ctx, rt, req)
	}
	candidate := jobdb.GoalCandidateFilter{}
	if req.Candidate != nil {
		candidate = *req.Candidate
	}
	ids, err := JobIDsInCells(ctx, rt, req.TenantId, cells, candidate.JobIDs)
	if err != nil {
		return jobdb.ListGoalsResponse{}, err
	}
	if len(ids) == 0 {
		return jobdb.ListGoalsResponse{Goals: []jobdb.GoalSummary{}}, nil
	}
	candidate.JobIDs = ids
	req.Candidate = &candidate
	return ListGoals(ctx, rt, req)
}

// JobIDsInCells resolves ownership using JobDB's first-class selector before administrative filtering.
func JobIDsInCells(ctx context.Context, rt jobdb.WorkflowRuntime, tenant string, cells, requested []string) ([]string, error) {
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
