package thinpackrebase

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

// B -> T1 -> T -> M; B -> F; C merges F and M and adds a resolution file.
// Fetching F and T completely proves B's ancestry through F while leaving M
// shallow. T is really an ancestor of C, but that path is invisible locally.
func TestReplaySelectionShallowMerge(t *testing.T) {
	for _, selector := range []string{"rebase", "thinpackrebase"} {
		for _, mode := range []string{"hidden-ancestor", "hidden-later", "hidden-target", "retrieved-target", "proven-ancestor", "proven-fast-forward", "complete-ancestor"} {
			t.Run(selector+"/"+mode, func(t *testing.T) {
				source := t.TempDir()
				initRepo(t, source)
				base := gitRevParse(t, source, "HEAD")
				commit := func(repo, name, content string) string {
					require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(content), 0644))
					runGit(t, repo, "git", "add", ".")
					runGit(t, repo, "git", "commit", "-m", name+content)
					return gitRevParse(t, repo, "HEAD")
				}
				commit(source, "README.md", "first\n")
				target := commit(source, "README.md", "second\n")
				mergeSide := commit(source, "upstream-extra", "extra\n")
				runGit(t, source, "git", "checkout", "-b", "candidate", base)
				feature := commit(source, "feature", "feature\n")
				runGit(t, source, "git", "merge", "--no-ff", "--no-commit", mergeSide)
				candidate := commit(source, "merge-resolution", "keep incorporated resolution\n")
				workspace := filepath.Join(t.TempDir(), "workspace")
				runGit(t, filepath.Dir(workspace), "git", "clone", "--depth", "2", "--branch", "candidate", "file://"+source, workspace)
				configureAuthor(t, workspace, "Worker", "worker@example.com")
				for _, hash := range []string{feature, target} {
					runGit(t, workspace, "git", "fetch", "--depth", "100", "origin", hash)
				}
				boundary, err := reachableShallowBoundary(context.Background(), workspace, candidate)
				require.NoError(t, err)
				require.Equal(t, mergeSide, boundary)
				switch mode {
				case "hidden-later":
					runGit(t, source, "git", "checkout", "--detach", target)
					target = commit(source, "later", "later\n")
					runGit(t, workspace, "git", "fetch", "--depth", "100", "origin", target)
				case "hidden-target", "retrieved-target":
					target = candidate
					runGit(t, workspace, "git", "checkout", "--detach", feature)
					candidate = commit(workspace, "pending", "pending\n")
					if mode == "hidden-target" {
						// Bounded retrieval would complete the target graph. Keep an
						// unavailable-history rejection and a separate success control.
						runGit(t, workspace, "git", "remote", "remove", "origin")
					}
				case "proven-ancestor":
					target = feature
				case "proven-fast-forward":
					target = candidate
					candidate = feature
					runGit(t, workspace, "git", "checkout", "--detach", candidate)
				case "complete-ancestor":
					runGit(t, workspace, "git", "fetch", "--depth", "100", "origin", mergeSide)
				}
				tree := gitRevParse(t, workspace, "HEAD^{tree}")
				deps := ops.NewOpDependenciesBuilder().WithWorktreePath(workspace).
					WithGitContext(ops.GitExecutionContext{ResolvedBaseHash: base, PersistHash: candidate}).Build()
				op := GetOp()
				if selector == "rebase" {
					op = GetRebaseOp()
				}
				out, err := op.TaskChain()[0].Invoke(deps, celltest.Context(context.Background()), map[string]any{
					"repo_path": workspace, "base_hash": base, "persist_hash": candidate, "target_base_hash": target,
				})
				if strings.HasPrefix(mode, "hidden-") {
					require.ErrorContains(t, err, "incomplete shallow history")
					var failure *Failure
					require.ErrorAs(t, err, &failure)
					require.Equal(t, FailedCode, failure.Code)
					require.Equal(t, "ancestry", failure.Details.Phase)
					require.True(t, failure.Details.WorkspaceRestored)
					require.Empty(t, failure.Details.Conflicts)
					require.Empty(t, deps.(interface{ WorkspaceBase() string }).WorkspaceBase())
					require.Equal(t, base, deps.GitContext().ResolvedBaseHash)
				} else {
					require.NoError(t, err)
					require.Equal(t, target, out["new_base_hash"])
					require.Equal(t, target, deps.(interface{ WorkspaceBase() string }).WorkspaceBase())
				}
				if mode == "retrieved-target" {
					require.Equal(t, target, gitRevParse(t, workspace, "HEAD^"))
					require.NotEqual(t, candidate, gitRevParse(t, workspace, "HEAD"))
					data, err := os.ReadFile(filepath.Join(workspace, "pending"))
					require.NoError(t, err)
					require.Equal(t, "pending\n", string(data))
				} else if mode == "proven-fast-forward" {
					require.Equal(t, target, gitRevParse(t, workspace, "HEAD"))
				} else {
					require.Equal(t, candidate, gitRevParse(t, workspace, "HEAD"))
					require.Equal(t, tree, gitRevParse(t, workspace, "HEAD^{tree}"))
				}
				if mode != "hidden-target" {
					data, err := os.ReadFile(filepath.Join(workspace, "merge-resolution"))
					require.NoError(t, err)
					require.Equal(t, "keep incorporated resolution\n", string(data))
				}
				require.NoError(t, requireCleanIntegration(context.Background(), workspace))
			})
		}
	}
}

func TestReplaySelectionSharedShallowBoundary(t *testing.T) {
	for _, selector := range []string{"rebase", "thinpackrebase"} {
		t.Run(selector, func(t *testing.T) {
			source := t.TempDir()
			initRepo(t, source)
			appendToFile(t, source, "README.md", "shared base\n")
			runGit(t, source, "git", "add", ".")
			runGit(t, source, "git", "commit", "-m", "shared base above hidden root")
			base := gitRevParse(t, source, "HEAD")
			appendToFile(t, source, "feature", "feature\n")
			runGit(t, source, "git", "add", ".")
			runGit(t, source, "git", "commit", "-m", "feature")
			candidate := gitRevParse(t, source, "HEAD")
			runGit(t, source, "git", "checkout", "-b", "target", base)
			appendToFile(t, source, "upstream", "upstream\n")
			runGit(t, source, "git", "add", ".")
			runGit(t, source, "git", "commit", "-m", "upstream")
			target := gitRevParse(t, source, "HEAD")
			workspace := filepath.Join(t.TempDir(), "workspace")
			runGit(t, filepath.Dir(workspace), "git", "clone", "--depth", "2", "file://"+source, workspace)
			runGit(t, workspace, "git", "fetch", "--depth", "2", "origin", candidate)
			runGit(t, workspace, "git", "checkout", "--detach", candidate)
			boundary, err := reachableShallowBoundary(context.Background(), workspace, candidate)
			require.NoError(t, err)
			require.Equal(t, base, boundary)
			deps := ops.NewOpDependenciesBuilder().WithWorktreePath(workspace).
				WithGitContext(ops.GitExecutionContext{ResolvedBaseHash: base, PersistHash: candidate}).Build()
			op := GetOp()
			if selector == "rebase" {
				op = GetRebaseOp()
			}
			out, err := op.TaskChain()[0].Invoke(deps, celltest.Context(context.Background()), map[string]any{
				"repo_path": workspace, "base_hash": base, "persist_hash": candidate, "target_base_hash": target,
				"git_author": "Worker <worker@example.com>",
			})
			require.NoError(t, err)
			require.Equal(t, target, out["new_base_hash"])
			require.Equal(t, target, gitRevParse(t, workspace, "HEAD^"))
			for _, name := range []string{"feature", "upstream"} {
				data, err := os.ReadFile(filepath.Join(workspace, name))
				require.NoError(t, err)
				require.Equal(t, name+"\n", string(data))
			}
			require.NoError(t, requireCleanIntegration(context.Background(), workspace))
		})
	}
}

// A shared shallow merge parent can sit above the older recorded base. Its
// hidden history is still excluded on both sides, even without a fetch remote.
func TestReplaySelectionSharedMergeBoundaryAboveBase(t *testing.T) {
	for _, selector := range []string{"rebase", "thinpackrebase"} {
		t.Run(selector, func(t *testing.T) {
			source := t.TempDir()
			initRepo(t, source)
			base := gitRevParse(t, source, "HEAD")
			commit := func(name string) string {
				appendToFile(t, source, name, name+"\n")
				runGit(t, source, "git", "add", ".")
				runGit(t, source, "git", "commit", "-m", name)
				return gitRevParse(t, source, "HEAD")
			}
			commit("hidden")
			mergeSide := commit("shared")
			runGit(t, source, "git", "checkout", "-b", "short", base)
			short := commit("short")
			runGit(t, source, "git", "merge", "--no-ff", mergeSide, "-m", "shared merge")
			shared := gitRevParse(t, source, "HEAD")
			candidate := commit("feature")
			runGit(t, source, "git", "checkout", "-b", "target", shared)
			target := commit("upstream")
			workspace := filepath.Join(t.TempDir(), "workspace")
			runGit(t, filepath.Dir(workspace), "git", "clone", "--depth", "3", "file://"+source, workspace)
			runGit(t, workspace, "git", "fetch", "--depth", "3", "origin", candidate)
			runGit(t, workspace, "git", "fetch", "--depth", "100", "origin", short)
			runGit(t, workspace, "git", "checkout", "--detach", candidate)
			boundary, err := reachableShallowBoundary(context.Background(), workspace, target)
			require.NoError(t, err)
			require.Equal(t, mergeSide, boundary)
			runGit(t, workspace, "git", "merge-base", "--is-ancestor", base, candidate)
			runGit(t, workspace, "git", "remote", "remove", "origin")
			deps := ops.NewOpDependenciesBuilder().WithWorktreePath(workspace).
				WithGitContext(ops.GitExecutionContext{ResolvedBaseHash: base, PersistHash: candidate}).Build()
			op := GetOp()
			if selector == "rebase" {
				op = GetRebaseOp()
			}
			out, err := op.TaskChain()[0].Invoke(deps, celltest.Context(context.Background()), map[string]any{
				"repo_path": workspace, "base_hash": base, "persist_hash": candidate, "target_base_hash": target,
				"git_author": "Worker <worker@example.com>",
			})
			require.NoError(t, err)
			require.Equal(t, target, out["new_base_hash"])
			require.Equal(t, target, gitRevParse(t, workspace, "HEAD^"))
			for _, name := range []string{"feature", "upstream", "short", "shared", "hidden"} {
				data, err := os.ReadFile(filepath.Join(workspace, name))
				require.NoError(t, err)
				require.Equal(t, name+"\n", string(data))
			}
			require.NoError(t, requireCleanIntegration(context.Background(), workspace))
		})
	}
}

// Candidate-side retrieval can reveal that the pinned target is incorporated.
// Selection computed while HEAD itself was shallow must not trigger replay after
// that retrieval; rewriting a merge drops resolution-only content.
func TestReplaySelectionAfterCandidateHistoryRetrieval(t *testing.T) {
	for _, selector := range []string{"rebase", "thinpackrebase"} {
		for _, topology := range []string{"linear", "merge"} {
			for _, mode := range []string{"default", "preserve", "reset"} {
				t.Run(selector+"/"+topology+"/"+mode, func(t *testing.T) {
					source := t.TempDir()
					initRepo(t, source)
					configureAuthor(t, source, "Feature Dev", "feature@example.test")
					base := gitRevParse(t, source, "HEAD")
					commit := func(name string) string {
						appendToFile(t, source, name, name+"\n")
						runGit(t, source, "git", "add", ".")
						runGit(t, source, "git", "commit", "-m", name)
						return gitRevParse(t, source, "HEAD")
					}
					target := commit("target")
					if topology == "merge" {
						runGit(t, source, "git", "checkout", "-b", "feature", base)
					}
					commit("feature")
					if topology == "merge" {
						runGit(t, source, "git", "merge", "--no-ff", "--no-commit", target)
					}
					candidate := commit("merge-resolution")
					workspace := filepath.Join(t.TempDir(), "workspace")
					runGit(t, filepath.Dir(workspace), "git", "clone", "--depth", "1", "file://"+source, workspace)
					runGit(t, workspace, "git", "fetch", "--depth", "100", "origin", target)
					boundary, err := reachableShallowBoundary(context.Background(), workspace, candidate)
					require.NoError(t, err)
					require.Equal(t, candidate, boundary)
					for _, ancestor := range []string{base, target} {
						known, err := isAncestor(context.Background(), workspace, ancestor, candidate)
						require.NoError(t, err)
						require.False(t, known, "candidate-side retrieval must reveal this ancestry")
					}
					tree := gitRevParse(t, workspace, "HEAD^{tree}")
					deps := ops.NewOpDependenciesBuilder().WithWorktreePath(workspace).
						WithGitContext(ops.GitExecutionContext{ResolvedBaseHash: base, PersistHash: candidate}).Build()
					input := map[string]any{"repo_path": workspace, "base_hash": base, "persist_hash": candidate, "target_base_hash": target, "git_author": "Workflow Bot <bot@example.test>", "update_refs": "refs/heads/result"}
					if mode != "default" {
						input["preserve_author"] = mode == "preserve"
					}
					op := GetOp()
					if selector == "rebase" {
						op = GetRebaseOp()
					}
					out, err := op.TaskChain()[0].Invoke(deps, celltest.Context(context.Background()), input)
					require.NoError(t, err)
					require.Equal(t, candidate, gitRevParse(t, workspace, "HEAD"))
					require.Equal(t, tree, gitRevParse(t, workspace, "HEAD^{tree}"))
					require.Equal(t, candidate, gitRevParse(t, workspace, "refs/heads/result"))
					require.Equal(t, target, out["new_base_hash"])
					require.Equal(t, candidate, out["new_persist_hash"])
					require.Equal(t, map[string]any{"base_hash": base, "persist_hash": candidate}, out["rebased_from"])
					require.Equal(t, target, deps.(interface{ WorkspaceBase() string }).WorkspaceBase())
					for _, ancestor := range []string{base, target} {
						known, err := isAncestor(context.Background(), workspace, ancestor, candidate)
						require.NoError(t, err)
						require.True(t, known, "candidate-side retrieval must prove this ancestry")
					}
					for _, name := range []string{"target", "feature", "merge-resolution"} {
						data, err := os.ReadFile(filepath.Join(workspace, name))
						require.NoError(t, err)
						require.Equal(t, name+"\n", string(data))
					}
					require.Equal(t, "Feature Dev <feature@example.test>", strings.TrimSpace(runGitOutput(t, workspace, "git", "log", "-1", "--format=%an <%ae>")))
					require.NoError(t, requireCleanIntegration(context.Background(), workspace))
				})
			}
		}
	}
}
