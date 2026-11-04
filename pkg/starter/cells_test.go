package starter_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestNamedCellSubmissionAndLeaseCeilings(t *testing.T) {
	ctx := celltest.Context(context.Background())
	rt := toy.New()
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	submitter := jobdbschema.WorkflowEngine{Engine: engine, Registry: rt}
	rec, err := recipe.LoadRecipeFromString([]byte("id: empty\nschema_version: 2\nsequence: []\n"))
	require.NoError(t, err)
	start := workflowctl.StartJob{TenantId: "tenant", JobID: "root-job", Cell: "root", CellRepo: "example.com/root", RecipeName: "empty"}
	_, err = starter.StartRecipeJob(ctx, start, submitter, *rec)
	require.ErrorIs(t, err, jobdb.ErrRootCellRequired)
	root, err := rt.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: "tenant", Name: "root", Repo: "example.com/root"})
	require.NoError(t, err)
	rootJob, err := starter.StartRecipeJob(ctx, start, submitter, *rec)
	require.NoError(t, err)
	require.Equal(t, root.ID, rootJob.CellID)
	lease, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: rootJob.Key(), WorkerID: "worker", Routes: []jobdb.Route{{JobType: "recipe"}}, LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.Equal(t, root.ID, lease.Job().CellID)
	child := jobdb.SubmitJob{TenantId: "tenant", JobID: "child", JobType: "recipe", Cell: "payments", CellRepo: "git@EXAMPLE.com:team/payments.git", Data: jobdb.NewTaskDataOrPanic(nil)}
	h, err := lease.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: child})
	require.NoError(t, err)
	require.Equal(t, "payments", h.Cell)
	require.NotEmpty(t, h.CellID)
	require.Equal(t, "example.com/team/payments", h.CellRepo)
	resolved, err := lease.ResolveCell(ctx, "payments")
	require.NoError(t, err)
	require.Equal(t, h.CellID, resolved.CellID)
	cell, err := rt.GetCell(ctx, jobdb.CellKey{TenantID: "tenant", CellID: h.CellID})
	require.NoError(t, err)
	repo := "example.com/team/payments-v2"
	_, err = rt.UpdateCell(ctx, jobdb.UpdateCellRequest{CellKey: jobdb.CellKey{TenantID: "tenant", CellID: cell.ID}, IfMatch: "\"" + cell.Revision + "\"", Repo: &repo})
	require.NoError(t, err)
	confirmed, err := lease.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: child})
	require.NoError(t, err)
	require.Equal(t, h.CellRepo, confirmed.CellRepo)
	child.JobID = "stale"
	_, err = lease.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: child})
	require.ErrorIs(t, err, jobdb.ErrCellRepoMismatch)
	child.JobID = "fresh"
	child.CellRepo = ""
	fresh, err := lease.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: child})
	require.NoError(t, err)
	require.Equal(t, repo, fresh.CellRepo)
	ordinary, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: h.JobKey, WorkerID: "worker", Routes: []jobdb.Route{{JobType: "recipe"}}, LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.Equal(t, h.CellRepo, ordinary.Job().CellRepo)
	child.JobID = "denied"
	child.Cell = "new-cell"
	child.CellRepo = "example.com/new-cell"
	_, err = ordinary.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: child})
	require.Error(t, err)
	page, err := rt.ListCells(ctx, jobdb.ListCellsRequest{TenantID: "tenant", Name: "new-cell"})
	require.NoError(t, err)
	require.Empty(t, page.Cells)
	missing := jobdb.SubmitJob{TenantId: "tenant", JobType: "recipe", Metadata: json.RawMessage(`{"cell_name":"root","cell_id":"forged","repo":"example.com/root"}`), Data: jobdb.NewTaskDataOrPanic(nil)}
	_, err = rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: missing})
	require.Error(t, err)
	summary, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, CellNames: []string{"payments"}})
	require.NoError(t, err)
	require.Len(t, summary.Jobs, 2)
}
