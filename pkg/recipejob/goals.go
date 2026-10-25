package recipejob

import (
	"context"
	"fmt"

	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// GoalReader is the authoritative read surface, separate from lifecycle writes.
type GoalReader interface {
	GetGoal(context.Context, jobdb.GoalKey) (jobdb.GoalInfo, error)
	GetDependencyWait(context.Context, jobdb.DependencyWaitKey) (jobdb.DependencyWaitInfo, error)
}

func GetGoal(ctx context.Context, reader GoalReader, key jobdb.GoalKey) (jobdb.GoalInfo, error) {
	if err := key.Validate(); err != nil {
		return jobdb.GoalInfo{}, err
	}
	return reader.GetGoal(ctx, key)
}

type GoalCandidatesRequest struct {
	TenantID  string
	GoalID    string
	PageSize  int
	PageToken string
}

// ListGoalCandidates includes all job types, primary and additional members,
// and archived/hidden candidates. The backend owns filtering and pagination.
// A page is not a complete candidate count or a source of goal state.
func ListGoalCandidates(ctx context.Context, lister Lister, req GoalCandidatesRequest) (joblist.Page, error) {
	if err := (jobdb.GoalKey{TenantId: req.TenantID, GoalID: req.GoalID}).Validate(); err != nil {
		return joblist.Page{}, err
	}
	if req.PageSize < 0 {
		return joblist.Page{}, fmt.Errorf("page size must not be negative")
	}
	page, err := lister.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{req.TenantID}, GoalIDs: []string{req.GoalID}, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}, IncludeHidden: true, PageSize: req.PageSize, PageToken: req.PageToken})
	if err != nil {
		return joblist.Page{}, err
	}
	out := joblist.Page{Jobs: make([]joblist.Job, 0, len(page.Jobs)), NextPageToken: page.NextPageToken}
	for _, j := range page.Jobs {
		out.Jobs = append(out.Jobs, joblist.JobFromSummary(j))
	}
	return out, nil
}

// AttachGoalCandidate and FailGoal require tenant lifecycle authority. These
// functions must not be exposed through a task's jobs.submit permission.
func AttachGoalCandidate(ctx context.Context, admin jobdb.GoalRuntime, req jobdb.AddGoalCandidateRequest) (jobdb.GoalCandidateInfo, error) {
	if err := req.GoalKey.Validate(); err != nil {
		return jobdb.GoalCandidateInfo{}, err
	}
	if req.JobID == "" {
		return jobdb.GoalCandidateInfo{}, fmt.Errorf("candidate job ID is required")
	}
	return admin.AddGoalCandidate(ctx, req)
}
func FailGoal(ctx context.Context, admin jobdb.GoalRuntime, req jobdb.FailGoalRequest) (jobdb.GoalInfo, error) {
	if err := req.Validate(); err != nil {
		return jobdb.GoalInfo{}, err
	}
	return admin.FailGoal(ctx, req)
}

// GoalDiscovery exposes tenant inspection separately from lifecycle authority.
// Requests and responses retain JobDB filters, counts, cursors and wait bindings.
type GoalDiscovery interface {
	ListGoals(context.Context, jobdb.ListGoalsRequest) (jobdb.ListGoalsResponse, error)
	ListDependencyWaits(context.Context, jobdb.ListDependencyWaitsRequest) (jobdb.ListDependencyWaitsResponse, error)
}

func ListGoals(ctx context.Context, reader GoalDiscovery, req jobdb.ListGoalsRequest) (jobdb.ListGoalsResponse, error) {
	if err := req.Validate(); err != nil {
		return jobdb.ListGoalsResponse{}, err
	}
	if req.PageSize < 0 {
		return jobdb.ListGoalsResponse{}, fmt.Errorf("page size must not be negative")
	}
	return reader.ListGoals(ctx, req)
}

// ListDependencyWaits retains consumer history. Target queries select only
// unresolved pending edges, never historical reverse dependencies.
func ListDependencyWaits(ctx context.Context, reader GoalDiscovery, req jobdb.ListDependencyWaitsRequest) (jobdb.ListDependencyWaitsResponse, error) {
	if err := req.Validate(); err != nil {
		return jobdb.ListDependencyWaitsResponse{}, err
	}
	if req.PageSize < 0 {
		return jobdb.ListDependencyWaitsResponse{}, fmt.Errorf("page size must not be negative")
	}
	return reader.ListDependencyWaits(ctx, req)
}

func GetDependencyWait(ctx context.Context, reader GoalReader, key jobdb.DependencyWaitKey) (jobdb.DependencyWaitInfo, error) {
	if key.JobKey.TenantId == "" || key.JobKey.JobId == "" || key.WaitID == "" {
		return jobdb.DependencyWaitInfo{}, fmt.Errorf("tenant, consumer job and wait ID are required")
	}
	return reader.GetDependencyWait(ctx, key)
}
