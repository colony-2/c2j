package recipejob

import (
	"context"
	"errors"
	"fmt"

	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// GoalReader retains source compatibility and historical dependency reads.
// Live goal helpers below reject without calling the backend.
type GoalReader interface {
	GetGoal(context.Context, jobdb.GoalKey) (jobdb.GoalInfo, error)
	GetDependencyWait(context.Context, jobdb.DependencyWaitKey) (jobdb.DependencyWaitInfo, error)
}

func GetGoal(ctx context.Context, reader GoalReader, key jobdb.GoalKey) (jobdb.GoalInfo, error) {
	return jobdb.GoalInfo{}, fmt.Errorf("goal queries are unsupported; select an exact job: %w", errors.ErrUnsupported)
}

type GoalCandidatesRequest struct {
	TenantID  string
	GoalID    string
	PageSize  int
	PageToken string
}

// ListGoalCandidates is retained for source compatibility and returns unsupported.
func ListGoalCandidates(ctx context.Context, lister Lister, req GoalCandidatesRequest) (joblist.Page, error) {
	return joblist.Page{}, fmt.Errorf("goal operations are unsupported; select an exact job: %w", errors.ErrUnsupported)
}

// GoalFailureAdmin is the tenant lifecycle capability, separate from reads and
// discovery. A task's jobs.submit permission must not grant this authority.
type GoalFailureAdmin interface {
	FailGoal(context.Context, jobdb.FailGoalRequest) (jobdb.GoalInfo, error)
}

func FailGoal(ctx context.Context, admin GoalFailureAdmin, req jobdb.FailGoalRequest) (jobdb.GoalInfo, error) {
	return jobdb.GoalInfo{}, fmt.Errorf("goal operations are unsupported; select an exact job: %w", errors.ErrUnsupported)
}

// GoalDiscovery retains legacy signatures; only exact dependency history is live.
type GoalDiscovery interface {
	ListGoals(context.Context, jobdb.ListGoalsRequest) (jobdb.ListGoalsResponse, error)
	ListDependencyWaits(context.Context, jobdb.ListDependencyWaitsRequest) (jobdb.ListDependencyWaitsResponse, error)
}

func ListGoals(ctx context.Context, reader GoalDiscovery, req jobdb.ListGoalsRequest) (jobdb.ListGoalsResponse, error) {
	return jobdb.ListGoalsResponse{}, fmt.Errorf("goal operations are unsupported; select an exact job: %w", errors.ErrUnsupported)
}

// ListDependencyWaits retains consumer history. Target queries select only
// unresolved pending edges, never historical reverse dependencies.
func ListDependencyWaits(ctx context.Context, reader GoalDiscovery, req jobdb.ListDependencyWaitsRequest) (jobdb.ListDependencyWaitsResponse, error) {
	if req.Target != nil && req.Target.GoalID != "" {
		return jobdb.ListDependencyWaitsResponse{}, fmt.Errorf("goal targets are unsupported: %w", errors.ErrUnsupported)
	}
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

func validateListGoals(req jobdb.ListGoalsRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if req.PageSize < 0 {
		return fmt.Errorf("page size must not be negative")
	}
	return nil
}
