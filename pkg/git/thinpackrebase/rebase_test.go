package thinpackrebase

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/gitstate"
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
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	for _, tc := range []struct{ name, cell, author, want string }{
		{"explicit", ".", "Workflow Bot <workflow@example.com>", "Workflow Bot <workflow@example.com>"},
		{"configured", ".", "", "Feature Dev <feature@example.com>"},
		{"root fallback", ".", "", "colony2.com <col2bot@colony2.com>"},
		{"named fallback", "workflow", "", "colony2.com <col2bot@colony2.com>"},
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

			if strings.Contains(tc.name, "fallback") {
				runGit(t, workspacePath, "git", "config", "--unset", "user.name")
				runGit(t, workspacePath, "git", "config", "--unset", "user.email")
			}
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

// The recorded base predates incorporated upstream history. Replaying from it
// would reapply the first upstream edit over the second, producing a false conflict.
func TestReplaySelectionUsesPinnedTarget(t *testing.T) {
	for _, relation := range []string{"ancestor", "later", "equal-base", "equal-candidate", "contained"} {
		for _, authorMode := range []string{"default", "preserve", "reset"} {
			preserve := authorMode != "reset"
			var preserveOption *bool
			if authorMode != "default" {
				preserveOption = &preserve
			}
			t.Run(relation+"/"+authorMode, func(t *testing.T) {
				repo := t.TempDir()
				initRepo(t, repo)
				base := gitRevParse(t, repo, "HEAD")
				for _, content := range []string{"first upstream\n", "second upstream\n"} {
					require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte(content), 0644))
					runGit(t, repo, "git", "add", ".")
					runGit(t, repo, "git", "commit", "-m", content)
				}
				incorporated := gitRevParse(t, repo, "HEAD")
				configureAuthor(t, repo, "Feature Dev", "feature@example.com")
				appendToFile(t, repo, "feature", "feature\n")
				runGit(t, repo, "git", "add", ".")
				runGit(t, repo, "git", "commit", "-m", "feature")
				retained := gitRevParse(t, repo, "HEAD")
				appendToFile(t, repo, "temporary-hook", "hook\n")
				runGit(t, repo, "git", "add", ".")
				runGit(t, repo, "git", "commit", "-m", "temporary hook")
				candidate := gitRevParse(t, repo, "HEAD")
				tree := gitRevParse(t, repo, "HEAD^{tree}")
				target := incorporated
				switch relation {
				case "equal-base":
					target = base
				case "equal-candidate":
					target = candidate
				case "later", "contained":
					parent := incorporated
					if relation == "contained" {
						parent = candidate
					}
					runGit(t, repo, "git", "checkout", "--detach", parent)
					appendToFile(t, repo, "later-upstream", "later\n")
					runGit(t, repo, "git", "add", ".")
					runGit(t, repo, "git", "commit", "-m", "later upstream")
					target = gitRevParse(t, repo, "HEAD")
					runGit(t, repo, "git", "checkout", "--detach", candidate)
				}
				out, err := Run(context.Background(), ThinpackRebaseInput{RepoPath: repo, BaseHash: base, PersistHash: candidate, TargetBaseHash: target, PreserveAuthor: preserveOption, GitAuthor: "Workflow Bot <bot@example.com>", UpdateRefs: "refs/heads/result"})
				require.NoError(t, err)
				require.Equal(t, RebasedFromSummary{BaseHash: base, PersistHash: candidate}, out.RebasedFrom)
				require.Equal(t, target, out.NewBaseHash)
				require.Equal(t, out.NewPersistHash, gitRevParse(t, repo, "refs/heads/result"))
				require.Equal(t, candidate, out.GitContextPatch["previous_hash"])
				switch relation {
				case "later":
					require.Equal(t, "2", strings.TrimSpace(runGitOutput(t, repo, "git", "rev-list", "--count", target+"..HEAD")))
					require.Equal(t, target, gitRevParse(t, repo, "HEAD~2"))
					want := "Feature Dev <feature@example.com>"
					if !preserve {
						want = "Workflow Bot <bot@example.com>"
					}
					for _, author := range strings.Split(strings.TrimSpace(runGitOutput(t, repo, "git", "log", "--format=%an <%ae>", target+"..HEAD")), "\n") {
						require.Equal(t, want, author)
					}
				case "contained":
					require.Equal(t, target, out.NewPersistHash)
				default:
					require.Equal(t, candidate, out.NewPersistHash)
					require.Equal(t, tree, gitRevParse(t, repo, "HEAD^{tree}"))
					require.Equal(t, "temporary-hook", strings.TrimSpace(runGitOutput(t, repo, "git", "diff", "--name-only", retained, "HEAD")))
					require.Equal(t, "1", strings.TrimSpace(runGitOutput(t, repo, "git", "rev-list", "--count", retained+"..HEAD")))
				}
				for name, content := range map[string]string{"README.md": "second upstream\n", "feature": "feature\n", "temporary-hook": "hook\n"} {
					data, err := os.ReadFile(filepath.Join(repo, name))
					require.NoError(t, err)
					require.Equal(t, content, string(data))
				}
				require.Empty(t, runGitOutput(t, repo, "git", "status", "--porcelain"))
			})
		}
	}
}

func TestReplaySelectionProvesShallowCandidateHistory(t *testing.T) {
	for _, available := range []bool{true, false} {
		t.Run(strconv.FormatBool(available), func(t *testing.T) {
			remote := filepath.Join(t.TempDir(), "remote")
			initRepo(t, remote)
			base := gitRevParse(t, remote, "HEAD")
			for _, content := range []string{"first\n", "second\n"} {
				require.NoError(t, os.WriteFile(filepath.Join(remote, "README.md"), []byte(content), 0644))
				runGit(t, remote, "git", "add", ".")
				runGit(t, remote, "git", "commit", "-m", content)
			}
			candidate := gitRevParse(t, remote, "HEAD")
			workspace := filepath.Join(t.TempDir(), "workspace")
			runGit(t, filepath.Dir(workspace), "git", "clone", "--depth", "1", "file://"+remote, workspace)
			runGit(t, workspace, "git", "fetch", "--depth", "1", "origin", base)
			if !available {
				runGit(t, workspace, "git", "remote", "remove", "origin")
			}
			out, err := Run(context.Background(), ThinpackRebaseInput{RepoPath: workspace, BaseHash: base, TargetBaseHash: base})
			if available {
				require.NoError(t, err)
				require.Equal(t, candidate, out.NewPersistHash)
			} else {
				require.Nil(t, out)
				require.ErrorContains(t, err, "candidate ancestry")
				require.ErrorContains(t, err, "incomplete shallow history")
			}
			require.Equal(t, candidate, gitRevParse(t, workspace, "HEAD"))
			require.Empty(t, runGitOutput(t, workspace, "git", "status", "--porcelain"))
		})
	}
}

func TestRebaseMissingTargetRetainsCompleteHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "source")
	initRepo(t, remote)
	original := gitRevParse(t, remote, "HEAD:README.md")
	require.NoError(t, os.WriteFile(filepath.Join(remote, "README.md"), []byte("selected base\n"), 0644))
	runGit(t, remote, "git", "commit", "-am", "base")
	base := gitRevParse(t, remote, "HEAD")
	workspace := filepath.Join(root, "workspace")
	task := &gitstate.GitTaskContext{
		GlobalGitTaskContext: &gitstate.GlobalGitTaskContext{BaseRepo: remote, BaseRef: base, ResolvedBaseHash: base, ParentHash: base, PersistHash: base},
		WorktreePath:         workspace,
	}
	require.NoError(t, gitstate.NewController(nil).Restore(ctx, task, nil))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "feature.txt"), []byte("feature\n"), 0644))
	runGit(t, workspace, "git", "add", "feature.txt")
	runGit(t, workspace, "git", "commit", "-m", "candidate")
	candidate := gitRevParse(t, workspace, "HEAD")

	require.NoError(t, os.WriteFile(filepath.Join(remote, "README.md"), []byte("intermediate\n"), 0644))
	runGit(t, remote, "git", "commit", "-am", "intermediate")
	intermediate := gitRevParse(t, remote, "HEAD")
	intermediateBlob := gitRevParse(t, remote, "HEAD:README.md")
	require.NoError(t, os.WriteFile(filepath.Join(remote, "README.md"), []byte("tip\n"), 0644))
	runGit(t, remote, "git", "commit", "-am", "advanced target")
	target := gitRevParse(t, remote, "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(remote, "feature.txt"), []byte("feature\n"), 0644))
	runGit(t, remote, "git", "add", "feature.txt")
	tree := strings.TrimSpace(runGitOutput(t, remote, "git", "write-tree"))
	runGit(t, remote, "git", "reset", "--hard", target)
	require.False(t, common.CommitExists(ctx, workspace, target))
	require.False(t, common.CommitExists(ctx, workspace, intermediate))
	_, err := common.ExecuteGitCommand(ctx, workspace, "cat-file", "-e", intermediateBlob)
	require.Error(t, err)

	input := ThinpackRebaseInput{RepoPath: workspace, TargetBaseHash: target, BaseHash: base, PersistHash: candidate, BaseRepo: remote, UpstreamRemote: "origin"}
	output, err := Run(ctx, input)
	require.NoError(t, err)
	require.Equal(t, target, output.NewBaseHash)
	require.Equal(t, target, gitRevParse(t, workspace, "HEAD^"))
	t.Logf("base=%s target=%s intermediate=%s originalBlob=%s intermediateBlob=%s tree=%s; reflog expire then gc --prune=now", base, target, intermediate, original, intermediateBlob, tree)
	runGit(t, workspace, "git", "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
	runGit(t, workspace, "git", "gc", "--prune=now")
	for _, ancestor := range []string{base, intermediate} {
		runGit(t, workspace, "git", "merge-base", "--is-ancestor", ancestor, "HEAD")
	}
	for _, blob := range []string{original, intermediateBlob} {
		runGit(t, workspace, "git", "cat-file", "-e", blob+"^{blob}")
	}
	require.Equal(t, tree, gitRevParse(t, workspace, "HEAD^{tree}"))
	require.Equal(t, "false", gitRevParse(t, workspace, "--is-shallow-repository"))
}
