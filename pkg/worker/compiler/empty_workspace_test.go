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
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestEmptyWorkspaceRecipePublicationAndParentIsolation(t *testing.T) {
	for _, separate := range []bool{false, true} {
		t.Run(fmt.Sprintf("separate=%t", separate), func(t *testing.T) { testEmptyWorkspacePublication(t, separate) })
	}
}

func testEmptyWorkspacePublication(t *testing.T, separate bool) {
	root := t.TempDir()
	a := filepath.Join(root, "A")
	b := filepath.Join(root, "B.git")
	original := workspaceRepo(t, a, "A")
	// An existing empty remote models a server-created uninitialized repository.
	require.NoError(t, runGit(root, "git", "init", "--bare", b))
	type hashes struct {
		Head string `json:"head" default:"{{ context.git.execution.head_hash }}"`
		Base string `json:"base" default:"{{ context.git.execution.base_hash }}"`
	}
	probe := coreops.NewActivityMappedOpV2[hashes, hashes](coreops.OpMetadata{Type: "execution_hash_probe"}, func(_ coreops.OpDependencies, _ context.Context, in hashes) (hashes, error) { return in, nil })
	ws := workspaceTestWorker(t, squashrebasemerge.GetOp(), squashrebasemerge.GetSquashOp(), squashrebasemerge.GetMergeOp(), probe)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: a, BaseRef: "main"}}
	text := fmt.Sprintf(`id: workspace-test
version: "1"
sequence:
 - id: create
   workspace: {cell: %q, ref: main, initial: empty}
   sequence:
    - id: readonly
      op: execution_hash_probe
    - id: discarded
      const: true
      op: workspace_probe
      inputs: {write: discarded.txt}
    - id: write
      op: workspace_probe
      inputs: {write: PURPOSE.md, absent: discarded.txt}
    - id: extra
      op: workspace_probe
      inputs: {write: extra.txt, read: PURPOSE.md}
    - id: publish
      op: squashrebasemerge
      inputs: {commit_message: "Approved initial purpose"}
    - id: read_published
      op: workspace_probe
      inputs: {read: PURPOSE.md}
    - id: update
      op: workspace_probe
      inputs: {write: update.txt}
    - id: publish_again
      op: squashrebasemerge
      inputs: {commit_message: "Following change"}
   outputs:
     first: "{{ sequence.publish.outputs.merged_hash }}"
     second: "{{ sequence.publish_again.outputs.merged_hash }}"
     unborn: "{{ sequence.readonly.outputs.head }}"
     restored: "{{ sequence.read_published.outputs.head }}"
 - id: parent
   op: workspace_probe
   inputs: {read: identity, absent: PURPOSE.md}
outputs:
 first: "{{ sequence.create.outputs.first }}"
 second: "{{ sequence.create.outputs.second }}"
 unborn: "{{ sequence.create.outputs.unborn }}"
 restored: "{{ sequence.create.outputs.restored }}"
 parent: "{{ sequence.parent.outputs.head }}"
`, b)
	if separate {
		text = strings.Replace(text, `    - id: publish
      op: squashrebasemerge
      inputs: {commit_message: "Approved initial purpose"}`, `    - id: squash
      op: squash
      inputs: {commit_message: "Approved initial purpose"}
    - id: restored_candidate
      op: workspace_probe
      inputs: {read: PURPOSE.md}
    - id: publish
      op: merge`, 1)
		text = strings.Replace(text, `    - id: publish_again
      op: squashrebasemerge
      inputs: {commit_message: "Following change"}`, `    - id: squash_again
      op: squash
      inputs: {commit_message: "Following change"}
    - id: publish_again
      op: merge`, 1)
	}
	out, _ := runWorkspaceRecipe(t, ws, text, job)
	require.Empty(t, out["unborn"])
	require.Equal(t, out["first"], out["restored"])
	require.Equal(t, original, out["parent"])
	head, err := common.GetCommitHash(context.Background(), b, "refs/heads/main")
	require.NoError(t, err)
	require.Equal(t, out["second"], head)
	parent, err := common.GetCommitHash(context.Background(), b, head+"^")
	require.NoError(t, err)
	require.Equal(t, out["first"], parent)
	roots, err := common.ExecuteGitCommand(context.Background(), b, "rev-list", "--max-parents=0", "main")
	require.NoError(t, err)
	require.Equal(t, out["first"].(string)+"\n", string(roots))
}

func TestEmptyWorkspaceResolutionAndReplay(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.git")
	workspaceTestWorker(t)
	rec, err := recipe.LoadRecipeFromString([]byte(fmt.Sprintf("id: empty\nworkspace: {cell: %q, ref: main, initial: empty}\nop: workspace_probe\n", missing)))
	require.NoError(t, err)
	job, commit := GenerateTestContext()
	stub := &workspaceReplayContext{}
	first, _, err := ExecuteRecipe(newWorkflowContext(stub), *rec, nil, job, commit)
	require.NoError(t, err)
	stub.position = 0
	second, _, err := ExecuteRecipe(newWorkflowContext(stub), *rec, nil, job, commit)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, stub.resolves)
	_, err = os.Stat(missing)
	require.True(t, os.IsNotExist(err))
	for _, initial := range []string{"", "typo"} {
		data, err := jobdb.NewTaskData(WorkspaceResolutionInput{Cell: missing, Ref: "main", ScopeID: "test", Initial: initial})
		require.NoError(t, err)
		_, err = resolveWorkspaceTask(context.Background(), data)
		require.Error(t, err)
	}
}
