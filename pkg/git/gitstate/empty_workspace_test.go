package gitstate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestEmptyWorkspaceLifecycle(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx := context.Background()
	remote := filepath.Join(t.TempDir(), "does-not-exist.git")
	task := &GitTaskContext{GlobalGitTaskContext: &GlobalGitTaskContext{
		BaseRepo: remote, BaseRef: "feature/creation", Workspace: &contextual.WorkspaceContext{Cell: "new", ScopeID: "scope", Initial: "empty"},
	}, WorktreePath: filepath.Join(t.TempDir(), "worktree")}
	controller := NewController(nil)
	require.NoError(t, controller.Restore(ctx, task, nil))
	branch, err := common.ExecuteGitCommand(ctx, task.WorktreePath, "symbolic-ref", "HEAD")
	require.NoError(t, err)
	require.Equal(t, "refs/heads/feature/creation", strings.TrimSpace(string(branch)))
	output, pack, err := controller.Persist(ctx, task)
	require.NoError(t, err)
	require.False(t, output.HasChanges)
	require.Empty(t, output.CommitHash)
	require.Nil(t, pack)
	// Simulate serialized task context being resumed on another worker, with no
	// access to any prior checkout (or remote, which still does not exist).
	moveWorker := func(pack jobdb.Artifact) {
		raw, err := json.Marshal(task.GlobalGitTaskContext)
		require.NoError(t, err)
		task = &GitTaskContext{GlobalGitTaskContext: &GlobalGitTaskContext{}, WorktreePath: filepath.Join(t.TempDir(), "worktree")}
		require.NoError(t, json.Unmarshal(raw, task.GlobalGitTaskContext))
		require.NoError(t, controller.Restore(ctx, task, pack))
	}
	moveWorker(nil)
	require.NoError(t, os.WriteFile(filepath.Join(task.WorktreePath, "PURPOSE.md"), []byte("purpose"), 0600))
	first, arts, err := controller.PersistWithDiffs(ctx, task)
	require.NoError(t, err)
	require.True(t, first.HasChanges)
	require.Empty(t, first.ParentHash)
	require.Empty(t, task.ResolvedBaseHash)
	require.Empty(t, task.ParentHash)
	require.NotEmpty(t, first.DiffFromParentPath)
	diff, err := os.ReadFile(first.DiffFromParentPath)
	require.NoError(t, err)
	require.Contains(t, string(diff), "+purpose")
	identity, err := common.ExecuteGitCommand(ctx, task.WorktreePath, "show", "-s", "--format=%an <%ae>|%cn <%ce>|%P", "HEAD")
	require.NoError(t, err)
	require.Equal(t, common.DefaultGitAuthor+"|"+common.DefaultGitAuthor+"|", strings.TrimSpace(string(identity)))
	findPack := func(arts []jobdb.Artifact) jobdb.Artifact {
		for _, a := range arts {
			if a.Name() == ThinPackArtifactName {
				return a
			}
		}
		t.Fatal("missing pack")
		return nil
	}
	firstPack := findPack(arts)
	moveWorker(firstPack)
	head, err := common.GetCommitHash(ctx, task.WorktreePath, "HEAD")
	require.NoError(t, err)
	require.Equal(t, first.CommitHash, head)
	content, err := os.ReadFile(filepath.Join(task.WorktreePath, "PURPOSE.md"))
	require.NoError(t, err)
	require.Equal(t, "purpose", string(content))
	require.NoError(t, os.WriteFile(filepath.Join(task.WorktreePath, "second.txt"), []byte("second"), 0600))
	second, pack, err := controller.Persist(ctx, task)
	require.NoError(t, err)
	require.Equal(t, first.CommitHash, second.ParentHash)
	moveWorker(pack)
	count, err := common.ExecuteGitCommand(ctx, task.WorktreePath, "rev-list", "--count", "HEAD")
	require.NoError(t, err)
	require.Equal(t, "2", strings.TrimSpace(string(count)))
	_, err = os.Stat(remote)
	require.True(t, os.IsNotExist(err), "preparation/persistence must not create the remote")
}

func TestEmptyWorkspaceCapturesRootCommittedByOperation(t *testing.T) {
	ctx := context.Background()
	task := &GitTaskContext{GlobalGitTaskContext: &GlobalGitTaskContext{BaseRepo: filepath.Join(t.TempDir(), "missing.git"), BaseRef: "main", Workspace: &contextual.WorkspaceContext{Initial: "empty"}}, WorktreePath: filepath.Join(t.TempDir(), "worktree")}
	c := NewController(nil)
	require.NoError(t, c.Restore(ctx, task, nil))
	require.NoError(t, os.WriteFile(filepath.Join(task.WorktreePath, "file"), []byte("content"), 0600))
	_, err := common.ExecuteGitCommand(ctx, task.WorktreePath, "add", ".")
	require.NoError(t, err)
	_, err = common.ExecuteGitCommand(ctx, task.WorktreePath, "commit", "-m", "operation root")
	require.NoError(t, err)
	head, err := common.GetCommitHash(ctx, task.WorktreePath, "HEAD")
	require.NoError(t, err)
	out, pack, err := c.Persist(ctx, task)
	require.NoError(t, err)
	require.Equal(t, head, out.CommitHash)
	require.Empty(t, out.ParentHash)
	require.NotNil(t, pack)
}

func TestOrdinaryWorkspaceDoesNotFallBackToEmpty(t *testing.T) {
	task := &GitTaskContext{GlobalGitTaskContext: &GlobalGitTaskContext{BaseRepo: filepath.Join(t.TempDir(), "missing.git"), BaseRef: "main"}, WorktreePath: filepath.Join(t.TempDir(), "worktree")}
	require.Error(t, NewController(nil).Restore(context.Background(), task, nil))
}
