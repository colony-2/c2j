package thinpackrebase

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestRunThinpackRebase_ReplaysCommits(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	remotePath := filepath.Join(tempDir, "remote")
	initRepo(t, remotePath)
	baseHash := gitRevParse(t, remotePath, "HEAD")

	workspacePath := filepath.Join(tempDir, "workspace")
	runGit(t, tempDir, "git", "clone", remotePath, workspacePath)
	configureAuthor(t, workspacePath, "Dev User", "dev@example.com")

	appendToFile(t, workspacePath, "app.txt", "workspace change\n")
	runGit(t, workspacePath, "git", "add", "app.txt")
	runGit(t, workspacePath, "git", "commit", "-m", "local change")
	originalPersist := gitRevParse(t, workspacePath, "HEAD")

	appendToFile(t, remotePath, "README.md", "upstream\n")
	runGit(t, remotePath, "git", "add", "README.md")
	runGit(t, remotePath, "git", "commit", "-m", "upstream change")
	targetBase := gitRevParse(t, remotePath, "HEAD")

	input := ThinpackRebaseInput{
		RepoPath:       workspacePath,
		TargetBaseHash: targetBase,
		UpstreamRemote: "origin",
		BaseHash:       baseHash,
		PersistHash:    originalPersist,
		BaseRepo:       remotePath,
		GitAuthor:      "Recipe Bot <bot@example.com>",
		CellName:       "alpha",
	}

	output, err := Run(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, output)

	require.Equal(t, targetBase, output.NewBaseHash)
	require.Equal(t, targetBase, output.TargetBaseHash)
	require.NotEqual(t, originalPersist, output.NewPersistHash)
	require.Equal(t, originalPersist, output.RebasedFrom.PersistHash)
	require.Equal(t, baseHash, output.RebasedFrom.BaseHash)
	require.Equal(t, targetBase, output.GitContextPatch["base_hash"])
	require.Equal(t, output.NewPersistHash, output.GitContextPatch["persist_hash"])
	require.Equal(t, originalPersist, output.GitContextPatch["previous_hash"])

	parent := gitRevParse(t, workspacePath, "HEAD^")
	require.Equal(t, targetBase, parent)
}

func TestRunThinpackRebase_ResetAuthor(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, cell, author, want string }{
		{"explicit", ".", "Workflow Bot <workflow@example.com>", "Workflow Bot <workflow@example.com>"},
		{"root fallback", ".", "", "c2j <c2j@colony2>"},
		{"named fallback", "workflow", "", "workflow <workflow@colony2>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			remotePath := filepath.Join(tempDir, "remote")
			initRepo(t, remotePath)
			baseHash := gitRevParse(t, remotePath, "HEAD")

			workspacePath := filepath.Join(tempDir, "workspace")
			runGit(t, tempDir, "git", "clone", remotePath, workspacePath)
			configureAuthor(t, workspacePath, "Feature Dev", "feature@example.com")

			appendToFile(t, workspacePath, "feature.txt", "change\n")
			runGit(t, workspacePath, "git", "add", "feature.txt")
			runGit(t, workspacePath, "git", "commit", "-m", "feature work")
			originalPersist := gitRevParse(t, workspacePath, "HEAD")

			appendToFile(t, remotePath, "README.md", "upstream\n")
			runGit(t, remotePath, "git", "add", "README.md")
			runGit(t, remotePath, "git", "commit", "-m", "upstream change")
			targetBase := gitRevParse(t, remotePath, "HEAD")

			preserve := false
			input := ThinpackRebaseInput{
				RepoPath:       workspacePath,
				TargetBaseHash: targetBase,
				UpstreamRemote: "origin",
				PreserveAuthor: &preserve,
				BaseHash:       baseHash,
				PersistHash:    originalPersist,
				BaseRepo:       remotePath,
				GitAuthor:      tc.author,
				CellName:       tc.cell,
			}

			output, err := Run(context.Background(), input)
			require.NoError(t, err)
			require.NotNil(t, output)

			author := strings.TrimSpace(runGitOutput(t, workspacePath, "git", "log", "-1", "--format=%an <%ae>"))
			require.Equal(t, tc.want, author)
		})
	}
}

func TestRunThinpackRebase_FastForwardWhenNoLocalCommits(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	remotePath := filepath.Join(tempDir, "remote")
	initRepo(t, remotePath)
	baseHash := gitRevParse(t, remotePath, "HEAD")

	workspacePath := filepath.Join(tempDir, "workspace")
	runGit(t, tempDir, "git", "clone", remotePath, workspacePath)
	configureAuthor(t, workspacePath, "Dev User", "dev@example.com")

	appendToFile(t, remotePath, "README.md", "upstream\n")
	runGit(t, remotePath, "git", "add", "README.md")
	runGit(t, remotePath, "git", "commit", "-m", "upstream change")
	targetBase := gitRevParse(t, remotePath, "HEAD")

	input := ThinpackRebaseInput{
		RepoPath:       workspacePath,
		TargetBaseHash: targetBase,
		UpstreamRemote: "origin",
		BaseHash:       baseHash,
		PersistHash:    baseHash,
		BaseRepo:       remotePath,
	}

	output, err := Run(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, output)

	head := gitRevParse(t, workspacePath, "HEAD")
	require.Equal(t, targetBase, head)
	require.Equal(t, targetBase, output.GitContextPatch["base_hash"])
	require.Equal(t, targetBase, output.GitContextPatch["persist_hash"])
}

func TestRunThinpackRebase_FailsWhenDiverged(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	remotePath := filepath.Join(tempDir, "remote")
	initRepo(t, remotePath)
	appendToFile(t, remotePath, "README.md", "main\n")
	runGit(t, remotePath, "git", "add", "README.md")
	runGit(t, remotePath, "git", "commit", "-m", "main update")
	baseHash := gitRevParse(t, remotePath, "HEAD")

	// create alternative history not descending from base
	ancestor := gitRevParse(t, remotePath, "HEAD^")
	runGit(t, remotePath, "git", "checkout", "-b", "alt", ancestor)
	appendToFile(t, remotePath, "alt.txt", "alt\n")
	runGit(t, remotePath, "git", "add", "alt.txt")
	runGit(t, remotePath, "git", "commit", "-m", "alternate")
	divergent := gitRevParse(t, remotePath, "HEAD")
	runGit(t, remotePath, "git", "checkout", "main")

	workspacePath := filepath.Join(tempDir, "workspace")
	runGit(t, tempDir, "git", "clone", remotePath, workspacePath)
	configureAuthor(t, workspacePath, "Dev User", "dev@example.com")

	appendToFile(t, workspacePath, "feature.txt", "change\n")
	runGit(t, workspacePath, "git", "add", "feature.txt")
	runGit(t, workspacePath, "git", "commit", "-m", "feature")
	persist := gitRevParse(t, workspacePath, "HEAD")

	input := ThinpackRebaseInput{
		RepoPath:       workspacePath,
		TargetBaseHash: divergent,
		UpstreamRemote: "origin",
		BaseHash:       baseHash,
		PersistHash:    persist,
		BaseRepo:       remotePath,
	}

	_, err := Run(context.Background(), input)
	require.Error(t, err)
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, filepath.Dir(dir), "git", "init", dir)
	configureAuthor(t, dir, "Main Dev", "main@example.com")
	appendToFile(t, dir, "README.md", "base\n")
	runGit(t, dir, "git", "add", "README.md")
	runGit(t, dir, "git", "commit", "-m", "initial")
	runGit(t, dir, "git", "checkout", "-B", "main")
}

func configureAuthor(t *testing.T, dir, name, email string) {
	t.Helper()
	runGit(t, dir, "git", "config", "user.name", name)
	runGit(t, dir, "git", "config", "user.email", email)
}

func appendToFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.OpenFile(full, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open file: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func runGit(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v (%s)", args, err, output)
	}
}

func runGitOutput(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v (%s)", args, err, output)
	}
	return string(output)
}

func gitRevParse(t *testing.T, dir, ref string) string {
	out := runGitOutput(t, dir, "git", "rev-parse", ref)
	return strings.TrimSpace(out)
}

// Exercise the topology created by gitshallow: both the target and old base
// exist, but depth-one boundaries hide their relationship.
func TestShallowRefresh(t *testing.T) {
	for _, tc := range []struct {
		name                                             string
		local, conflict, divergent, missingTarget, dirty bool
	}{
		{name: "fast_forward"}, {name: "pending_commits", local: true},
		{name: "fetch_target", missingTarget: true},
		{name: "conflict", local: true, conflict: true},
		{name: "divergence", local: true, divergent: true},
		{name: "dirty_workspace", dirty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := filepath.Join(t.TempDir(), "remote")
			initRepo(t, remote)
			appendToFile(t, remote, "old.txt", "old\n")
			runGit(t, remote, "git", "add", ".")
			runGit(t, remote, "git", "commit", "-m", "base")
			base := gitRevParse(t, remote, "HEAD")
			if tc.divergent {
				runGit(t, remote, "git", "checkout", "-b", "other", "HEAD^")
			}
			appendToFile(t, remote, "README.md", "upstream\n")
			runGit(t, remote, "git", "add", ".")
			runGit(t, remote, "git", "commit", "-m", "target")
			target := gitRevParse(t, remote, "HEAD")
			workspace := filepath.Join(t.TempDir(), "workspace")
			if tc.missingTarget {
				runGit(t, remote, "git", "checkout", "-B", "old", base)
			}
			runGit(t, filepath.Dir(workspace), "git", "clone", "--depth", "1", "file://"+remote, workspace)
			runGit(t, workspace, "git", "fetch", "--depth", "1", "origin", base)
			runGit(t, workspace, "git", "checkout", base)
			configureAuthor(t, workspace, "Dev", "dev@example.com")
			if tc.local {
				name := "pending.txt"
				if tc.conflict {
					name = "README.md"
				}
				appendToFile(t, workspace, name, "local\n")
				runGit(t, workspace, "git", "add", ".")
				runGit(t, workspace, "git", "commit", "-m", "pending")
			}
			if tc.dirty {
				appendToFile(t, workspace, "dirty.txt", "keep\n")
			}
			head := gitRevParse(t, workspace, "HEAD")
			require.Equal(t, "true", strings.TrimSpace(runGitOutput(t, workspace, "git", "rev-parse", "--is-shallow-repository")))
			out, err := Run(context.Background(), ThinpackRebaseInput{RepoPath: workspace, BaseHash: base, TargetBaseHash: target})
			if tc.conflict || tc.divergent || tc.dirty {
				require.Error(t, err)
				require.Equal(t, head, gitRevParse(t, workspace, "HEAD"))
				if tc.conflict {
					require.Contains(t, err.Error(), "rebase failed")
					require.Empty(t, strings.TrimSpace(runGitOutput(t, workspace, "git", "status", "--porcelain")))
				}
				if tc.divergent {
					require.Contains(t, err.Error(), "does not descend")
				}
				if tc.local {
					require.Contains(t, runGitOutput(t, workspace, "git", "show", "HEAD"), "local")
				}
				if tc.dirty {
					data, e := os.ReadFile(filepath.Join(workspace, "dirty.txt"))
					require.NoError(t, e)
					require.Equal(t, "keep\n", string(data))
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, target, out.NewBaseHash)
			require.Equal(t, "true", strings.TrimSpace(runGitOutput(t, workspace, "git", "rev-parse", "--is-shallow-repository")), "refresh should retain shallow history")
			if tc.local {
				require.Equal(t, target, gitRevParse(t, workspace, "HEAD^"))
				data, e := os.ReadFile(filepath.Join(workspace, "pending.txt"))
				require.NoError(t, e)
				require.Equal(t, "local\n", string(data))
			} else {
				require.Equal(t, target, gitRevParse(t, workspace, "HEAD"))
			}
			again, e := Run(context.Background(), ThinpackRebaseInput{RepoPath: workspace, BaseHash: target, TargetBaseHash: target})
			require.NoError(t, e)
			require.Equal(t, out.NewPersistHash, again.NewPersistHash)
		})
	}
}

func TestShallowRefreshWithoutHistorySource(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote")
	initRepo(t, remote)
	base := gitRevParse(t, remote, "HEAD")
	appendToFile(t, remote, "README.md", "next\n")
	runGit(t, remote, "git", "add", ".")
	runGit(t, remote, "git", "commit", "-m", "next")
	target := gitRevParse(t, remote, "HEAD")
	workspace := filepath.Join(t.TempDir(), "workspace")
	runGit(t, filepath.Dir(workspace), "git", "clone", "--depth", "1", "file://"+remote, workspace)
	runGit(t, workspace, "git", "fetch", "--depth", "1", "origin", base)
	runGit(t, workspace, "git", "checkout", base)
	runGit(t, workspace, "git", "remote", "remove", "origin")
	_, err := Run(context.Background(), ThinpackRebaseInput{RepoPath: workspace, BaseHash: base, TargetBaseHash: target})
	require.ErrorContains(t, err, "incomplete shallow history")
	require.Equal(t, base, gitRevParse(t, workspace, "HEAD"))
}

func TestManagedRefreshCannotBypassWorkspaceFloor(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	old := gitRevParse(t, repo, "HEAD")
	appendToFile(t, repo, "README.md", "new\n")
	runGit(t, repo, "git", "add", ".")
	runGit(t, repo, "git", "commit", "-m", "initial workspace")
	initial := gitRevParse(t, repo, "HEAD")
	deps := ops.NewOpDependenciesBuilder().WithWorktreePath(repo).WithGitContext(ops.GitExecutionContext{ResolvedBaseHash: initial}).Build()
	_, err := NewThinpackRebaseActivity().Execute(deps, context.Background(), ThinpackRebaseInput{RepoPath: repo, BaseHash: old, TargetBaseHash: old})
	require.ErrorContains(t, err, "workspace base constraint")
	require.Equal(t, initial, gitRevParse(t, repo, "HEAD"))
}
