package localruntime

import (
	"context"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSelectedSubmissionUsesTrustedLocalAdministrator(t *testing.T) {
	rt := New(toy.New())
	ctx := context.Background()
	_, err := rt.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: "t", Name: "work", Repo: "example.test/work"})
	require.NoError(t, err)
	req := jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "t", JobID: "source", Cell: "work", CellRepo: "example.test/work", JobType: "work", Data: jobdb.NewTaskDataOrPanic(nil)}}
	source, err := rt.SubmitJob(ctx, req)
	require.NoError(t, err)
	req.Job.JobID = "selected"
	req.Job.GoalID = source.GoalID
	selected, err := rt.SubmitJob(ctx, req)
	require.NoError(t, err)
	require.Equal(t, source.GoalID, selected.GoalID)
	again, err := rt.SubmitJob(ctx, req)
	require.NoError(t, err)
	require.Equal(t, selected, again)
}
