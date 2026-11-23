package recipejob

import (
	"context"
	"errors"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExactJobRetiredQueriesNeverDispatch(t *testing.T) {
	ctx := context.Background()
	_, e := GetGoal(ctx, nil, jobdb.GoalKey{TenantId: "t", GoalID: "G"})
	require.ErrorIs(t, e, errors.ErrUnsupported)
	_, e = ListGoals(ctx, nil, jobdb.ListGoalsRequest{TenantId: "t"})
	require.ErrorIs(t, e, errors.ErrUnsupported)
	_, e = ListGoalCandidates(ctx, nil, GoalCandidatesRequest{TenantID: "t", GoalID: "G"})
	require.ErrorIs(t, e, errors.ErrUnsupported)
	_, e = FailGoal(ctx, nil, jobdb.FailGoalRequest{GoalKey: jobdb.GoalKey{TenantId: "t", GoalID: "G"}, Message: "stop"})
	require.ErrorIs(t, e, errors.ErrUnsupported)
	_, e = ListDependencyWaits(ctx, nil, jobdb.ListDependencyWaitsRequest{TenantId: "t", Target: &jobdb.DependencyTarget{GoalID: "G"}})
	require.ErrorIs(t, e, errors.ErrUnsupported)
	_, e = BuildListJobsRequest(ListRecipeJobsRequest{TenantID: "t", GoalIDs: []string{"G"}})
	require.ErrorContains(t, e, "unsupported")
}
