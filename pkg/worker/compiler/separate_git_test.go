package compiler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/squashrebasemerge"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestSeparateGitOpsRestorePreparedCandidate(t *testing.T) {
	for _, alternate := range []bool{false, true} {
		t.Run(fmt.Sprintf("alternate_destination=%t", alternate), func(t *testing.T) { testSeparateGitOps(t, alternate) })
	}
}

func testSeparateGitOps(t *testing.T, alternate bool) {
	ctx := context.Background()
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "target.git")
	initial := workspaceRepo(t, source, "source")
	require.NoError(t, runGit(root, "git", "clone", "--bare", source, remote))
	type result struct {
		Hash string `json:"hash"`
	}
	advance := coreops.NewActivityMappedOpV2[struct{}, result](coreops.OpMetadata{Type: "advance_target"}, func(_ coreops.OpDependencies, _ context.Context, _ struct{}) (result, error) {
		if err := os.WriteFile(filepath.Join(source, "upstream.txt"), []byte("upstream"), 0644); err != nil {
			return result{}, err
		}
		for _, args := range [][]string{{"git", "add", "."}, {"git", "commit", "-m", "upstream change"}, {"git", "push", remote, "HEAD:refs/heads/main"}} {
			if err := runGit(source, args[0], args[1:]...); err != nil {
				return result{}, err
			}
		}
		hash, err := common.GetCommitHash(ctx, source, "HEAD")
		return result{Hash: hash}, err
	})
	ws := workspaceTestWorker(t, squashrebasemerge.GetSquashOp(), thinpackrebase.GetRebaseOp(), squashrebasemerge.GetMergeOp(), advance)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "source"}, GitBase: contextual.GitBaseContext{BaseRepo: remote, BaseRef: "main"}}
	text := `id: workspace-test
sequence:
 - id: first
   op: workspace_probe
   inputs: {write: first.txt}
 - id: second
   op: workspace_probe
   inputs: {write: second.txt}
 - id: squash
   op: squash
   inputs: {commit_message: "Prepared changes"}
 - id: advance
   op: advance_target
 - id: rebase
   op: rebase
   inputs: {target_base_hash: "{{ sequence.advance.outputs.hash }}"}
 - id: restored
   op: workspace_probe
   inputs: {read: first.txt}
 - id: merge
   op: merge
 - id: published
   op: workspace_probe
   inputs: {read: upstream.txt}
outputs:
 squashed: "{{ sequence.squash.outputs.squashed_hash }}"
 rebased: "{{ sequence.rebase.outputs.new_persist_hash }}"
 restored: "{{ sequence.restored.outputs.head }}"
 published: "{{ sequence.published.outputs.head }}"
 merged: "{{ sequence.merge.outputs.merged_hash }}"
 target: "{{ sequence.advance.outputs.hash }}"
`
	destination := remote
	if alternate {
		destination = filepath.Join(root, "alternate.git")
		require.NoError(t, runGit(root, "git", "clone", "--bare", source, destination))
		text = strings.Replace(text, "   op: merge\n", fmt.Sprintf("   op: merge\n   inputs: {upstream_repo: %q}\n", destination), 1)
	}
	out, _ := runWorkspaceRecipe(t, ws, text, job)
	if alternate {
		sourceHead, err := common.GetCommitHash(ctx, remote, "refs/heads/main")
		require.NoError(t, err)
		require.Equal(t, out["target"], sourceHead)
	}
	require.NotEqual(t, out["squashed"], out["rebased"])
	require.Equal(t, out["rebased"], out["restored"])
	require.Equal(t, out["rebased"], out["merged"])
	require.Equal(t, out["merged"], out["published"])
	head, err := common.GetCommitHash(ctx, destination, "refs/heads/main")
	require.NoError(t, err)
	require.Equal(t, out["merged"], head)
	parent, err := common.GetCommitHash(ctx, destination, head+"^")
	require.NoError(t, err)
	require.Equal(t, out["target"], parent)
	grandparent, err := common.GetCommitHash(ctx, destination, head+"^^")
	require.NoError(t, err)
	require.Equal(t, initial, grandparent)
	for _, file := range []string{"first.txt", "second.txt", "upstream.txt"} {
		_, err := common.ExecuteGitCommand(ctx, destination, "show", fmt.Sprintf("%s:%s", head, file))
		require.NoError(t, err)
	}
}
