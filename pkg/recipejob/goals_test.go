package recipejob

import (
	"context"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

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
	out, err := ListGoalCandidates(context.Background(), goalCandidateLister{t}, GoalCandidatesRequest{TenantID: "t", GoalID: "g", PageSize: 7, PageToken: "cursor"})
	require.NoError(t, err)
	require.Equal(t, "next", out.NextPageToken)
	require.Len(t, out.Jobs, 1)
	require.Equal(t, "primary", out.Jobs[0].GoalID)
	require.Empty(t, out.Jobs[0].OtherGoalIDs, "membership filtering remains server-side; lists omit memberships")
	require.Equal(t, "external", out.Jobs[0].JobType)
	_, err = ListGoalCandidates(context.Background(), nil, GoalCandidatesRequest{TenantID: "t"})
	require.Error(t, err)
}
