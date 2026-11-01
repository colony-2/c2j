package compiler

import (
	"context"
	"fmt"
	"os"
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

func TestFloatingWorkspaceTaskRetryCanObserveNewBase(t *testing.T) {
	testWorkspaceTaskRetry(t, false)
}

func TestPinnedWorkspaceTaskRetryKeepsCandidate(t *testing.T) {
	testWorkspaceTaskRetry(t, true)
}

func testWorkspaceTaskRetry(t *testing.T, pinned bool) {
	t.Helper()
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
			if err := os.WriteFile(filepath.Join(d.WorktreePath(), "failed-edit"), []byte("must not be accepted"), 0600); err != nil {
				return nil, err
			}
			if err := runGit(repo, "git", "commit", "--allow-empty", "-m", "advance"); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("transient failure")
		}
		if _, err := os.Stat(filepath.Join(d.WorktreePath(), "failed-edit")); !os.IsNotExist(err) {
			return nil, fmt.Errorf("retry inherited failed edits: %v", err)
		}
		return map[string]any{"head": head}, nil
	})
	ws := workspaceTestWorker(t, op)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "repo"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	write := ""
	if pinned {
		write = " - id: write\n   op: workspace_probe\n   inputs: {write: candidate}\n"
	}
	out, _ := runWorkspaceRecipe(t, ws, fmt.Sprintf(`id: workspace-test
workspace: {cell: %q}
sequence:
%s - id: retried
   op: retry_workspace
   retry: {maximum_attempts: 2, initial_interval: 1ms}
 - id: fresh
   op: workspace_probe
outputs: {head: '${{ sequence.fresh.outputs.head }}'}
`, repo, write), job)
	require.Len(t, heads, 2)
	if pinned {
		require.NotEqual(t, initial, heads[0])
		require.Equal(t, heads[0], heads[1])
	} else {
		require.Equal(t, initial, heads[0])
		require.NotEqual(t, initial, heads[1])
	}
	require.Equal(t, heads[1], out["head"])
}

type workspaceChainOutput struct {
	First  string `json:"first"`
	Second string `json:"second"`
	Base   string `json:"base"`
	Text   string `json:"text"`
}

func TestFloatingWorkspaceTaskChainPinsOnlyAfterWrite(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprintf("write=%t", write), func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			initial := workspaceRepo(t, repo, "repo")
			op, err := coreops.NewOp().WithType("workspace_chain").
				AddStep("workspace_chain", coreops.NewStepWithDeps(func(d coreops.OpDependencies, ctx context.Context, _ struct{}) (workspaceChainOutput, error) {
					head, err := common.GetCommitHash(ctx, d.WorktreePath(), "HEAD")
					if err != nil {
						return workspaceChainOutput{}, err
					}
					if write {
						if err := os.WriteFile(filepath.Join(d.WorktreePath(), "candidate"), []byte("local change"), 0600); err != nil {
							return workspaceChainOutput{}, err
						}
					}
					if err := runGit(repo, "git", "commit", "--allow-empty", "-m", "advance"); err != nil {
						return workspaceChainOutput{}, err
					}
					return workspaceChainOutput{First: head}, nil
				})).
				AddStep("second", coreops.NewStepWithDeps(func(d coreops.OpDependencies, ctx context.Context, in workspaceChainOutput) (workspaceChainOutput, error) {
					head, err := common.GetCommitHash(ctx, d.WorktreePath(), "HEAD")
					in.Second = head
					in.Base = d.GitContext().ResolvedBaseHash
					if write && err == nil {
						var data []byte
						data, err = os.ReadFile(filepath.Join(d.WorktreePath(), "candidate"))
						in.Text = string(data)
					}
					return in, err
				})).Build()
			require.NoError(t, err)
			ws := workspaceTestWorker(t, op.(coreops.RegisterableOp))
			job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "repo"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
			out, _ := runWorkspaceRecipe(t, ws, fmt.Sprintf(`id: workspace-test
workspace: {cell: %q}
sequence:
 - id: chain
   op: workspace_chain
outputs: {result: '${{ sequence.chain.outputs }}'}
`, repo), job)
			result := out["result"].(map[string]any)
			require.Equal(t, initial, result["first"])
			advanced, err := common.GetCommitHash(context.Background(), repo, "HEAD")
			require.NoError(t, err)
			if write {
				require.Equal(t, initial, result["base"])
				require.NotEqual(t, initial, result["second"])
				require.NotEqual(t, advanced, result["second"])
				require.Equal(t, "local change", result["text"])
			} else {
				require.Equal(t, advanced, result["base"])
				require.Equal(t, advanced, result["second"])
			}
		})
	}
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

func TestFloatingWorkspaceHookTasksPinOnlyAfterWrite(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprintf("write=%t", write), func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "repo")
			writeInput := ""
			if write {
				writeInput = ", write: candidate"
			}
			initial := commitHookFile(t, repo, ".c2/hooks/check.yaml", fmt.Sprintf(`id: check
sequence:
 - id: advance
   op: workspace_probe
   inputs: {advance: %q%s}
 - id: inspect
   op: workspace_probe
outputs: {first: '${{ sequence.advance.outputs.head }}', second: '${{ sequence.inspect.outputs.head }}'}
`, repo, writeInput))
			ws := workspaceTestWorker(t)
			job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "repo"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
			out, _ := runWorkspaceRecipe(t, ws, fmt.Sprintf(`id: workspace-test
workspace: {cell: %q}
sequence:
 - id: hook
   include: cell://.c2/hooks/check.yaml
 - id: after
   op: workspace_probe
outputs: {hook: '${{ sequence.hook.outputs }}', after: '${{ sequence.after.outputs.head }}'}
`, repo), job)
			hook := out["hook"].(map[string]any)
			require.Equal(t, initial, hook["first"])
			require.NotEqual(t, initial, hook["second"])
			require.Equal(t, hook["second"], out["after"])
			advanced, err := common.GetCommitHash(context.Background(), repo, "HEAD")
			require.NoError(t, err)
			if write {
				require.NotEqual(t, advanced, hook["second"])
			} else {
				require.Equal(t, advanced, hook["second"])
			}
		})
	}
}
