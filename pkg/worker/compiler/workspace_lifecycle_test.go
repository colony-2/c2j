package compiler

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestFloatingWorkspaceExplicitRefreshAdoptsBaseWithoutChanges(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initial := workspaceRepo(t, repo, "repo")
	ws := workspaceTestWorker(t, thinpackrebase.GetRebaseOp())
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "repo"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	out, _ := runWorkspaceRecipe(t, ws, fmt.Sprintf(`id: workspace-test
workspace: {cell: %q}
sequence:
 - id: select
   op: rebase
   inputs: {target_base_hash: %q}
 - id: advance
   op: workspace_probe
   inputs: {advance: %q}
 - id: next
   op: workspace_probe
outputs: {head: '${{ sequence.next.outputs.head }}'}
`, repo, initial, repo), job)
	require.Equal(t, initial, out["head"])
	advanced, err := common.GetCommitHash(context.Background(), repo, "HEAD")
	require.NoError(t, err)
	require.NotEqual(t, initial, advanced)
}

func TestFloatingWorkspaceTaskRetryKeepsObservedBase(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initial := workspaceRepo(t, repo, "repo")
	var heads []string
	op := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "retry_workspace"}, func(d coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
		head, err := common.GetCommitHash(ctx, d.WorktreePath(), "HEAD")
		if err != nil {
			return nil, err
		}
		heads = append(heads, head)
		if len(heads) == 1 {
			if err := runGit(repo, "git", "commit", "--allow-empty", "-m", "advance"); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("transient failure")
		}
		return map[string]any{"head": head}, nil
	})
	ws := workspaceTestWorker(t, op)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "repo"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	out, _ := runWorkspaceRecipe(t, ws, fmt.Sprintf(`id: workspace-test
workspace: {cell: %q}
sequence:
 - id: retried
   op: retry_workspace
   retry: {maximum_attempts: 2, initial_interval: 1ms}
 - id: fresh
   op: workspace_probe
outputs: {head: '${{ sequence.fresh.outputs.head }}'}
`, repo), job)
	require.Equal(t, []string{initial, initial}, heads)
	require.NotEqual(t, initial, out["head"])
}

func TestFloatingWorkspaceReadOnlyHookDoesNotAcceptBase(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "repo")
	initial := commitHookFile(t, repo, ".c2/hooks/check.yaml", `id: check
inputs: {candidate: '${{ inputs.candidate }}'}
sequence:
 - id: inspect
   op: workspace_probe
outputs: {head: '${{ sequence.inspect.outputs.head }}', candidate: '${{ inputs.candidate }}'}
`)
	ws := workspaceTestWorker(t)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "repo"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	out, _ := runWorkspaceRecipe(t, ws, fmt.Sprintf(`id: workspace-test
workspace: {cell: %q}
sequence:
 - id: hook
   include: cell://.c2/hooks/check.yaml
   inputs: {candidate: '${{ context.git.hash }}'}
 - id: advance
   op: workspace_probe
   inputs: {advance: %q}
 - id: fresh
   op: workspace_probe
outputs: {hook: '${{ sequence.hook.outputs }}', head: '${{ sequence.fresh.outputs.head }}'}
`, repo, repo), job)
	hook := out["hook"].(map[string]any)
	require.Equal(t, initial, hook["head"])
	require.Equal(t, initial, hook["candidate"])
	require.NotEqual(t, initial, out["head"])
}
