package recipejob

import (
	"context"
	"errors"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

// Historical reader data models secondary membership created by older backends;
// the current test never constructs it through a live mutation.
type goalCandidateLister struct{ t *testing.T }

func (l goalCandidateLister) ListJobs(_ context.Context, r jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	require.Equal(l.t, []string{"t"}, r.TenantIds)
	require.Equal(l.t, []string{"g"}, r.GoalIDs)
	require.True(l.t, r.IncludeHidden)
	require.ElementsMatch(l.t, []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}, r.Stores)
	require.Empty(l.t, r.Statuses)
	require.Empty(l.t, r.JobTypes)
	require.Equal(l.t, 7, r.PageSize)
	require.Equal(l.t, "cursor", r.PageToken)
	return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{{JobKey: jobdb.JobKey{TenantId: "t", JobId: "external-candidate"}, JobType: "external", GoalID: "primary", OtherGoalIDs: []string{"g"}, Status: jobdb.JobStatusCompleted}}, NextPageToken: "next"}, nil
}
func TestGoalCandidatesIncludeAdditionalArchivedNonRecipeMembers(t *testing.T) {
	out, err := ListGoalCandidates(celltest.Context(context.Background()), goalCandidateLister{t}, GoalCandidatesRequest{TenantID: "t", GoalID: "g", PageSize: 7, PageToken: "cursor"})
	require.NoError(t, err)
	require.Equal(t, "next", out.NextPageToken)
	require.Len(t, out.Jobs, 1)
	require.Equal(t, "primary", out.Jobs[0].GoalID)
	require.Empty(t, out.Jobs[0].OtherGoalIDs, "membership filtering remains server-side; lists omit memberships")
	require.Equal(t, "external", out.Jobs[0].JobType)
	_, err = ListGoalCandidates(celltest.Context(context.Background()), nil, GoalCandidatesRequest{TenantID: "t"})
	require.Error(t, err)
}

type failureOnly struct {
	call func(context.Context, jobdb.FailGoalRequest) (jobdb.GoalInfo, error)
}

func (f failureOnly) FailGoal(ctx context.Context, req jobdb.FailGoalRequest) (jobdb.GoalInfo, error) {
	return f.call(ctx, req)
}
func TestFailGoalRequiresOnlyFailureCapability(t *testing.T) {
	ctx := context.Background()
	req := jobdb.FailGoalRequest{GoalKey: jobdb.GoalKey{TenantId: "t", GoalID: "g"}, Message: "  exhausted café\n"}
	want := jobdb.GoalInfo{GoalKey: req.GoalKey, State: jobdb.GoalFailed, Failure: &jobdb.GoalFailure{Message: req.Message}}
	calls := 0
	var failure error
	admin := failureOnly{call: func(got context.Context, request jobdb.FailGoalRequest) (jobdb.GoalInfo, error) {
		calls++
		require.Equal(t, ctx, got)
		require.Equal(t, req, request)
		return want, failure
	}}
	for _, invalid := range []jobdb.FailGoalRequest{
		{Message: "reason"}, {GoalKey: jobdb.GoalKey{TenantId: "t"}, Message: "reason"},
		{GoalKey: jobdb.GoalKey{GoalID: "g"}, Message: "reason"},
		{GoalKey: req.GoalKey}, {GoalKey: req.GoalKey, Message: " \n\t"},
	} {
		_, err := FailGoal(ctx, admin, invalid)
		require.Error(t, err)
	}
	require.Zero(t, calls)
	got, err := FailGoal(ctx, admin, req)
	require.NoError(t, err)
	require.Equal(t, want, got)
	failure = errors.New("backend failure")
	_, err = FailGoal(ctx, admin, req)
	require.Same(t, failure, err)
	require.Equal(t, 2, calls)
	_, err = GetGoal(ctx, nil, jobdb.GoalKey{})
	require.Error(t, err)
	_, err = ListGoalCandidates(ctx, nil, GoalCandidatesRequest{TenantID: "t", GoalID: "g", PageSize: -1})
	require.Error(t, err)
}
