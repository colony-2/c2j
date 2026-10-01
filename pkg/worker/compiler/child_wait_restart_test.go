package compiler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// This replaces process orchestration in the recipes dependency harness. The
// database survives worker replacement; neither recipe nor selector mocks can
// manufacture the pending/cancelled transitions under test.
func TestChildWaitSurvivesWorkerReplacementAndCancellation(t *testing.T) {
	ctx := context.Background()
	var before, after atomic.Int32
	ids := make(chan string, 2)
	probe := coreops.NewActivityMappedOpV2[struct {
		Child string `json:"child"`
	}, map[string]any](coreops.OpMetadata{Type: "wait_probe"}, func(deps coreops.OpDependencies, ctx context.Context, in struct {
		Child string `json:"child"`
	}) (map[string]any, error) {
		if in.Child != "" {
			before.Add(1)
			ids <- in.Child
		} else {
			after.Add(1)
		}
		return map[string]any{}, nil
	})
	hold := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "hold_child"}, func(deps coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
		deps.SetNextTaskType("test.external.hold")
		return map[string]any{}, nil
	})
	workspaceTestWorker(t, probe, hold)
	repo := t.TempDir()
	workspaceRepo(t, repo, "root")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".c2j/recipes"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".c2j/recipes/child.yaml"), []byte(`id: child
version: "1"
op: hold_child
`), 0644))
	require.NoError(t, runGit(repo, "git", "add", "."))
	require.NoError(t, runGit(repo, "git", "commit", "-m", "child"))
	db := filepath.Join(t.TempDir(), "jobs.db")
	eng, stop := objectEngineAt(t, db)
	rec, err := recipe.LoadRecipeFromString([]byte(`id: parent
version: "1"
sequence:
- id: submit
  op: recipes.run
  inputs:
    recipes: [{name: child}]
- id: before
  op: wait_probe
  inputs: {child: "${{ sequence.submit.outputs.job_ids[0] }}"}
- id: wait
  op: recipe.await_result_soft
  inputs: {job_id: "${{ sequence.submit.outputs.job_ids[0] }}"}
- id: after
  op: wait_probe
outputs:
  child: ${{ sequence.submit.outputs.job_ids[0] }}
  result: ${{ sequence.wait.outputs }}
`))
	require.NoError(t, err)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "root"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "object-tests", RecipeName: "parent", JobContext: job, GitRef: "main"}, eng, *rec)
	require.NoError(t, err)
	var child string
	select {
	case child = <-ids:
	case <-time.After(10 * time.Second):
		t.Fatal("parent did not submit child")
	}
	require.Eventually(t, func() bool {
		info, err := eng.GetJob(ctx, key)
		return err == nil && info.Status == jobdb.JobStatusPendingJobs
	}, 10*time.Second, 10*time.Millisecond)
	require.Zero(t, after.Load(), "parent must not pass the wait while its child is pending")
	require.Eventually(t, func() bool {
		jobs, err := eng.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{key.TenantId}, JobKeys: []jobdb.JobKey{{TenantId: key.TenantId, JobId: child}}})
		return err == nil && len(jobs.Jobs) == 1 && jobs.Jobs[0].NextRoute != nil && jobs.Jobs[0].NextRoute.TaskType == "test.external.hold"
	}, 10*time.Second, 10*time.Millisecond)
	stop()
	eng, _ = objectEngineAt(t, db)
	require.NoError(t, eng.CancelJob(ctx, jobdb.CancelJob{JobKey: jobdb.JobKey{TenantId: key.TenantId, JobId: child}}))
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 10*time.Second, key, eng))
	result, err := swfutil.JobResult(ctx, eng, key)
	require.NoError(t, err)
	data, err := result.GetData()
	require.NoError(t, err)
	var out struct {
		Child  string `json:"child"`
		Result struct {
			Status   string `json:"status"`
			Terminal bool   `json:"terminal"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(data, &out))
	require.Equal(t, child, out.Child)
	require.Equal(t, "cancelled", out.Result.Status)
	require.True(t, out.Result.Terminal)
	require.EqualValues(t, 1, before.Load(), "worker replacement must replay completed parent ops")
	require.EqualValues(t, 1, after.Load())
	children, err := eng.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{key.TenantId}, ParentJobIDs: []string{key.JobId}, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}})
	require.NoError(t, err)
	require.Len(t, children.Jobs, 1, "worker replacement must not duplicate child submission")
	require.Equal(t, child, children.Jobs[0].JobKey.JobId)
}

func TestAwaitAlreadyFinishedChildAfterWorkerReplacement(t *testing.T) {
	ctx := context.Background()
	workspaceTestWorker(t)
	repo := t.TempDir()
	workspaceRepo(t, repo, "root")
	db := filepath.Join(t.TempDir(), "jobs.db")
	eng, stop := objectEngineAt(t, db)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "root"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	child, err := recipe.LoadRecipeFromString([]byte("id: child\nversion: '1'\nsequence: []\noutputs: {value: retained}\n"))
	require.NoError(t, err)
	key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "object-tests", RecipeName: "child", JobContext: job, GitRef: "main"}, eng, *child)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 10*time.Second, key, eng))
	stop()
	eng, _ = objectEngineAt(t, db)
	out := runObjectRecipe(t, eng, `id: parent
version: "1"
input_schema:
  child: {type: string, required: true}
inputs: {child: "${{ inputs.child }}"}
sequence:
- id: await
  op: recipe.await_result_soft
  inputs: {job_id: "${{ inputs.child }}"}
outputs:
  status: ${{ sequence.await.outputs.status }}
  terminal: ${{ sequence.await.outputs.terminal }}
  outputs: ${{ sequence.await.outputs.outputs }}
`, job, map[string]any{"child": key.JobId})
	require.Equal(t, "completed", out["status"])
	require.Equal(t, true, out["terminal"])
	require.Equal(t, "retained", out["outputs"].(map[string]any)["value"])
}
