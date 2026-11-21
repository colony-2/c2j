package recipejob

import (
	"context"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
)

type catalogCapture struct{ calls int }

func (c *catalogCapture) SubmitJobRef(_ context.Context, j jobdb.SubmitJob) (jobref.Ref, error) {
	c.calls++
	return jobref.Ref{TenantID: j.TenantId, JobID: "accepted", GoalID: "goal"}, nil
}
func TestExecutionBaseCatalogLibraryRequiredAndZeroPolicy(t *testing.T) {
	p := executioncatalog.Policy{Required: true}
	req := BuildStartJobRequest{BaseCatalog: &p, TenantID: "tenant", Target: ResolvedTarget{RepositorySource: "example.test/app", CellName: "app"}, Recipe: "build"}
	_, err := BuildStartJob(req)
	require.NoError(t, err, "preparation must not read live authority")
	capture := &catalogCapture{}
	_, err = SubmitRecipeJob(context.Background(), req, capture)
	require.Error(t, err)
	require.Zero(t, capture.calls)
	req.BaseCatalog = nil
	_, err = SubmitRecipeJob(context.Background(), req, capture)
	require.NoError(t, err)
	require.Equal(t, 1, capture.calls)
}
