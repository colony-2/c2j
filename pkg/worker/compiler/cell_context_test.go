package compiler

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestCellRenameAndRepositoryEditPreserveResumeAndReplay(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, schema := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/v%d", backend, schema), func(t *testing.T) {
				ctx := celltest.Context(context.Background())
				repo := filepath.Join(t.TempDir(), "repo")
				workspaceRepo(t, repo, "original")
				commitHookFile(t, repo, ".c2/hooks/check.yaml", "id: check\nsequence: []\noutputs: {}\n")
				workspaceTestWorker(t)
				rt := newTimeoutReproRuntime(t, backend)
				key := submitGitLifecycle(t, rt, repo, fmt.Sprintf(`id: git-lifecycle
schema_version: %d
sequence:
 - id: hook
   include: cell://.c2/hooks/check.yaml
 - id: read
   op: workspace_probe
   inputs: {read: identity}
outputs:
 text: '${{ sequence.read.outputs.text }}'
 owner: '${{ context.workflow.cell }}'
 owner_id: '${{ context.workflow.cell_id }}'
`, schema))
				fault := &gitOutcomeInterruption{WorkflowRuntime: rt, match: `task:"cell-context"`}
				_, err := runGitLifecycle(t, fault, key)
				require.Error(t, err)
				require.True(t, fault.fired)
				page, err := rt.ListCells(ctx, jobdb.ListCellsRequest{TenantID: key.TenantId, Name: "test"})
				require.NoError(t, err)
				require.Len(t, page.Cells, 1)
				cell := page.Cells[0]
				name, newRepo := "renamed", "example.test/new-repository"
				_, err = rt.UpdateCell(ctx, jobdb.UpdateCellRequest{CellKey: jobdb.CellKey{TenantID: key.TenantId, CellID: cell.ID}, IfMatch: `"` + cell.Revision + `"`, Name: &name, Repo: &newRepo})
				require.NoError(t, err)
				out, err := runGitLifecycle(t, rt, key)
				require.NoError(t, err)
				output := gitLifecycleOutput(t, out)
				require.Equal(t, "original", output["text"])
				require.Equal(t, cell.ID, output["owner_id"])
				require.Equal(t, "test", output["owner"], "recorded canonical name survives a rename")
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{})).BuildEngine()
				require.NoError(t, err)
				replay, err := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
				require.NoError(t, err)
				raw, err := replay.GetData()
				require.NoError(t, err)
				original, err := out.Output.GetData()
				require.NoError(t, err)
				require.JSONEq(t, string(original), string(raw))
			})
		}
	}
}
