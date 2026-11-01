package workspacesubmit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return string(out)
}
func write(t *testing.T, dir, name, value string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(value), 0600))
}
func repo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	write(t, dir, "tracked", "base")
	write(t, dir, "deleted", "delete me")
	write(t, dir, ".gitignore", "ignored\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")
	head, err := git(context.Background(), dir, nil, "rev-parse", "HEAD")
	require.NoError(t, err)
	return dir, head
}
func source(dir, base string) Source {
	return Source{Worktree: dir, Git: gitstate.GlobalGitTaskContext{BaseRepo: dir, BaseRef: "main", ResolvedBaseHash: base, CellName: "cell", Workspace: &contextual.WorkspaceContext{Cell: "cell", ScopeID: "parent", InitialHash: base, RefPolicy: contextual.WorkspaceFollowUntilChange}}}
}
func startJob() workflowctl.StartJob {
	return workflowctl.StartJob{TenantId: "tenant", RecipeName: "child"}
}
func restore(t *testing.T, start workflowctl.StartJob) string {
	t.Helper()
	j := start.JobContext
	c := j.InitialCommit
	dir := filepath.Join(t.TempDir(), "restored")
	task := &gitstate.GitTaskContext{WorktreePath: dir, GlobalGitTaskContext: &gitstate.GlobalGitTaskContext{BaseRepo: j.GitBase.BaseRepo, BaseRef: j.GitBase.BaseRef, ResolvedBaseHash: j.GitBase.ResolvedBaseHash, Workspace: j.Workspace, PersistHash: c.PersistHash, ParentHash: c.ParentHash, ParentRef: c.ParentRef}}
	var a jobdb.Artifact
	if len(start.Artifacts) > 0 {
		a = start.Artifacts[len(start.Artifacts)-1]
	}
	require.NoError(t, gitstate.NewController(nil).Restore(context.Background(), task, a))
	return dir
}
func TestLiveSnapshotRestoresAllChangesWithoutMutatingSource(t *testing.T) {
	ctx := context.Background()
	upstream, base := repo(t)
	local := filepath.Join(t.TempDir(), "local")
	runGit(t, upstream, "clone", upstream, local)
	runGit(t, local, "config", "user.name", "Test")
	runGit(t, local, "config", "user.email", "test@example.com")
	write(t, local, "committed", "unpublished")
	runGit(t, local, "add", "committed")
	runGit(t, local, "commit", "-m", "local")
	write(t, local, "tracked", "staged")
	runGit(t, local, "add", "tracked")
	write(t, local, "tracked", "unstaged")
	write(t, local, "new", "new file")
	write(t, local, "ignored", "excluded")
	require.NoError(t, os.Remove(filepath.Join(local, "deleted")))
	before := runGit(t, local, "status", "--porcelain=v1")
	head := runGit(t, local, "rev-parse", "HEAD")
	refs := runGit(t, local, "show-ref")
	index, err := os.ReadFile(filepath.Join(local, ".git", "index"))
	require.NoError(t, err)
	src := source(upstream, base)
	src.Worktree = local
	start, err := src.Apply(ctx, startJob(), Now, false)
	require.NoError(t, err)
	require.Equal(t, before, runGit(t, local, "status", "--porcelain=v1"))
	require.Equal(t, head, runGit(t, local, "rev-parse", "HEAD"))
	require.Equal(t, refs, runGit(t, local, "show-ref"))
	indexAfter, err := os.ReadFile(filepath.Join(local, ".git", "index"))
	require.NoError(t, err)
	require.Equal(t, index, indexAfter)
	write(t, local, "tracked", "later")
	for range 2 {
		dst := restore(t, start)
		for name, want := range map[string]string{"tracked": "unstaged", "new": "new file", "committed": "unpublished"} {
			data, err := os.ReadFile(filepath.Join(dst, name))
			require.NoError(t, err)
			require.Equal(t, want, string(data))
		}
		require.NoFileExists(t, filepath.Join(dst, "ignored"))
		require.NoFileExists(t, filepath.Join(dst, "deleted"))
	}
}
func TestTaskStartExcludesLiveEditsAndPinsFloatingBase(t *testing.T) {
	dir, base := repo(t)
	src := source(dir, base)
	write(t, dir, "tracked", "during task")
	start, err := src.Apply(context.Background(), startJob(), Start, false)
	require.NoError(t, err)
	require.Empty(t, start.Artifacts)
	require.Empty(t, start.JobContext.Workspace.RefPolicy)
	dst := restore(t, start)
	data, err := os.ReadFile(filepath.Join(dst, "tracked"))
	require.NoError(t, err)
	require.Equal(t, "base", string(data))
	// The starting mode does not even require the live worktree to exist.
	src.Worktree = "/does/not/exist"
	_, err = src.Apply(context.Background(), startJob(), Start, false)
	require.NoError(t, err)
}
func TestSnapshotReuseAndCleanAdvancedHead(t *testing.T) {
	ctx := context.Background()
	dir, base := repo(t)
	src := source(dir, base)
	clean, err := src.Apply(ctx, startJob(), Now, false)
	require.NoError(t, err)
	require.Empty(t, clean.Artifacts)
	require.Equal(t, base, clean.GitRef)
	write(t, dir, "tracked", "previous task")
	prior, err := src.Apply(ctx, startJob(), Now, false)
	require.NoError(t, err)
	incoming := restore(t, prior)
	src.Worktree = incoming
	src.Git.PersistHash = prior.GitRef
	src.Git.ParentHash = base
	src.Artifact = prior.Artifacts[0]
	key := jobdb.ArtifactKey{}
	src.RestoreArtifact = &key
	reused, err := src.Apply(ctx, startJob(), Now, false)
	require.NoError(t, err)
	require.Empty(t, reused.Artifacts)
	require.Equal(t, &key, reused.JobContext.RestoreArtifact)
	require.Equal(t, prior.GitRef, reused.GitRef)
	write(t, incoming, "tracked", "current task")
	start, err := src.Apply(ctx, startJob(), Start, false)
	require.NoError(t, err)
	require.Equal(t, prior.GitRef, start.GitRef)
	runGit(t, incoming, "add", ".")
	runGit(t, incoming, "commit", "-m", "advance")
	advanced := runGit(t, incoming, "rev-parse", "HEAD")
	now, err := src.Apply(ctx, startJob(), Now, false)
	require.NoError(t, err)
	require.Equal(t, advanced, now.GitRef+"\n")
	require.Len(t, now.Artifacts, 1)
	dst := restore(t, now)
	data, err := os.ReadFile(filepath.Join(dst, "tracked"))
	require.NoError(t, err)
	require.Equal(t, "current task", string(data))
}
func TestCaptureErrorsDoNotSubmitOrChangeWorktree(t *testing.T) {
	ctx := context.Background()
	dir, base := repo(t)
	src := source(dir, base)
	bad := startJob()
	bad.JobContext.GitBase.BaseRepo = "https://example.com/other.git"
	bad.JobContext.Workflow.CellName = "other"
	_, err := src.Apply(ctx, bad, Now, true)
	require.ErrorContains(t, err, "does not match")
	src.Git.PersistHash = base
	_, err = src.Apply(ctx, startJob(), Start, false)
	require.ErrorContains(t, err, "artifact is unavailable")
	src.Git.PersistHash = ""
	// Install conflict stages without changing the working files.
	blob := runGit(t, dir, "rev-parse", "HEAD:tracked")
	cmd := exec.Command("git", "update-index", "--index-info")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("0 0000000000000000000000000000000000000000\ttracked\n100644 " + strings.TrimSpace(blob) + " 1\ttracked\n100644 " + strings.TrimSpace(blob) + " 2\ttracked\n")
	out, e := cmd.CombinedOutput()
	require.NoError(t, e, "%s", out)
	_, err = src.Apply(ctx, startJob(), Now, false)
	require.ErrorContains(t, err, "unresolved merge conflicts")
	_, err = src.Apply(ctx, startJob(), Start, false)
	require.NoError(t, err)
}

func TestLiveSnapshotFromShallowWorktree(t *testing.T) {
	ctx := context.Background()
	upstream, _ := repo(t)
	write(t, upstream, "tracked", "next base")
	runGit(t, upstream, "add", ".")
	runGit(t, upstream, "commit", "-m", "next")
	base, err := git(ctx, upstream, nil, "rev-parse", "HEAD")
	require.NoError(t, err)
	local := filepath.Join(t.TempDir(), "shallow")
	runGit(t, upstream, "clone", "--depth=1", "file://"+upstream, local)
	write(t, local, "tracked", "shallow change")
	src := source(upstream, base)
	src.Worktree = local
	start, err := src.Apply(ctx, startJob(), Now, false)
	require.NoError(t, err)
	dst := restore(t, start)
	data, err := os.ReadFile(filepath.Join(dst, "tracked"))
	require.NoError(t, err)
	require.Equal(t, "shallow change", string(data))
}

func TestEmptyTaskWorkspaceSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	src := source(dir, "")
	src.Git.Workspace.Initial = "empty"
	for _, mode := range []string{Start, Now} {
		job, err := src.Apply(ctx, startJob(), mode, false)
		require.NoError(t, err)
		require.Empty(t, job.Artifacts)
		require.Empty(t, job.JobContext.InitialCommit.PersistHash)
	}
	write(t, dir, "new", "first content")
	job, err := src.Apply(ctx, startJob(), Now, false)
	require.NoError(t, err)
	dst := restore(t, job)
	data, err := os.ReadFile(filepath.Join(dst, "new"))
	require.NoError(t, err)
	require.Equal(t, "first content", string(data))
}
