package permissions

import (
	"context"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtimetest"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSelectedGoalRequiresIndependentDelegatedAuthority(t *testing.T) {
	calls := 0
	ctx := runtimetest.PrimaryGoalContext(context.Background())
	access := NewAccess(ctx, JobDB{Jobs: JobPermissions{Submit: true}}, nil, func(context.Context, jobdb.SubmitJob) (jobref.Ref, error) { calls++; return jobref.Ref{}, nil })
	_, err := access.Submit(ctx, jobdb.SubmitJob{TenantId: "t", JobID: "dest", GoalID: "goal", Cell: "work"})
	require.ErrorIs(t, err, jobdb.ErrPrimaryGoalSelectionForbidden)
	require.Zero(t, calls)
	_, err = access.Submit(ctx, jobdb.SubmitJob{TenantId: "t", JobID: "ordinary", Cell: "work"})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}
