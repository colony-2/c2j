package thinpackrebase

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func adoptionFixture(t *testing.T) ThinpackRebaseInput {
	t.Helper()
	repo, base, target, _ := conflictFixture(t, "file", "text")
	// A later resolution cannot rescue replay of the earlier conflicting commit.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "file"), []byte("reconciled upstream and local\n"), 0600))
	runGit(t, repo, "git", "rm", "README.md")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "binary\nfile"), []byte{0, 255, 128, 10, 0}, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "executable"), []byte("#!/bin/sh\nexit 0\n"), 0755))
	require.NoError(t, os.Symlink("file", filepath.Join(repo, "symbolic link")))
	runGit(t, repo, "git", "add", ".")
	runGit(t, repo, "git", "commit", "--author", "Resolver <resolver@example.com>", "-m", "Completed resolution\n\nA second paragraph.\n")
	return ThinpackRebaseInput{RepoPath: repo, BaseHash: base, PersistHash: gitRevParse(t, repo, "HEAD"), TargetBaseHash: target,
		Resolution: &Resolution{TreeHash: gitRevParse(t, repo, "HEAD^{tree}")}, GitAuthor: "Worker <worker@example.com>"}
}

func adoptionDeps(input ThinpackRebaseInput) ops.OpDependencies {
	return ops.NewOpDependenciesBuilder().WithWorktreePath(input.RepoPath).
		WithGitContext(ops.GitExecutionContext{ResolvedBaseHash: input.BaseHash, PersistHash: input.PersistHash}).Build()
}

func TestResolvedTreeVersusConflictingReplay(t *testing.T) {
	for _, selector := range []string{"rebase", "thinpackrebase"} {
		t.Run(selector, func(t *testing.T) {
			input := adoptionFixture(t)
			replay := input
			replay.Resolution = nil
			_, err := Run(celltest.Context(context.Background()), replay)
			var conflict *Failure
			require.ErrorAs(t, err, &conflict)
			require.Equal(t, ConflictCode, conflict.Code)
			require.True(t, conflict.Details.WorkspaceRestored)
			require.Equal(t, input.PersistHash, gitRevParse(t, input.RepoPath, "HEAD"))
			require.NoError(t, requireCleanIntegration(context.Background(), input.RepoPath))
			require.NotEmpty(t, conflict.evidence["conflict-index.z"])
			old := runGitOutput(t, input.RepoPath, "git", "cat-file", "commit", input.PersistHash)
			deps := adoptionDeps(input)
			op := GetOp()
			if selector == "rebase" {
				op = GetRebaseOp()
			}
			raw := map[string]any{"repo_path": input.RepoPath, "base_hash": input.BaseHash, "persist_hash": input.PersistHash,
				"target_base_hash": input.TargetBaseHash, "resolution": map[string]any{"tree_hash": input.Resolution.TreeHash}, "git_author": input.GitAuthor}
			result, err := op.TaskChain()[0].Invoke(deps, celltest.Context(context.Background()), raw)
			require.NoError(t, err)
			head := gitRevParse(t, input.RepoPath, "HEAD")
			require.Equal(t, input.Resolution.TreeHash, gitRevParse(t, input.RepoPath, "HEAD^{tree}"))
			require.Equal(t, input.TargetBaseHash, strings.TrimSpace(runGitOutput(t, input.RepoPath, "git", "show", "-s", "--format=%P", head)))
			require.Equal(t, input.TargetBaseHash, result["new_base_hash"])
			require.Equal(t, input.TargetBaseHash, result["target_base_hash"])
			require.Equal(t, head, result["new_persist_hash"])
			require.Equal(t, input.TargetBaseHash, deps.(interface{ WorkspaceBase() string }).WorkspaceBase())
			prior := result["rebased_from"].(map[string]any)
			require.Equal(t, input.BaseHash, prior["base_hash"])
			require.Equal(t, input.PersistHash, prior["persist_hash"])
			require.Equal(t, map[string]any{"base_hash": input.TargetBaseHash, "persist_hash": head, "previous_hash": input.PersistHash}, result["git_context_patch"])
			fresh := runGitOutput(t, input.RepoPath, "git", "cat-file", "commit", head)
			_, oldMessage, _ := strings.Cut(old, "\n\n")
			_, newMessage, _ := strings.Cut(fresh, "\n\n")
			require.Equal(t, oldMessage, newMessage)
			for _, line := range strings.Split(old, "\n") {
				if strings.HasPrefix(line, "author ") {
					require.Contains(t, fresh, line+"\n")
				}
			}
			data, err := os.ReadFile(filepath.Join(input.RepoPath, "binary\nfile"))
			require.NoError(t, err)
			require.Equal(t, []byte{0, 255, 128, 10, 0}, data)
			link, err := os.Readlink(filepath.Join(input.RepoPath, "symbolic link"))
			require.NoError(t, err)
			require.Equal(t, "file", link)
			require.Contains(t, runGitOutput(t, input.RepoPath, "git", "ls-tree", "HEAD", "executable"), "100755")
			require.NoFileExists(t, filepath.Join(input.RepoPath, "README.md"))
			require.Equal(t, "1.2.1", op.GetMetadata().Version)
		})
	}
}

func TestAdoptionBoundaryCasesAndRefs(t *testing.T) {
	for _, mode := range []string{"equal-tree", "unchanged-base", "configured-author", "update-existing-ref", "update-new-ref", "target-omission"} {
		t.Run(mode, func(t *testing.T) {
			input := adoptionFixture(t)
			repo := input.RepoPath
			switch mode {
			case "equal-tree":
				runGit(t, repo, "git", "read-tree", "--reset", "-u", input.TargetBaseHash)
				runGit(t, repo, "git", "commit", "-m", "resolution equals target")
			case "unchanged-base":
				input.TargetBaseHash = input.BaseHash
			case "configured-author":
				keep := false
				input.PreserveAuthor = &keep
			case "update-existing-ref":
				input.UpdateRefs = "refs/heads/adopted"
				runGit(t, repo, "git", "update-ref", input.UpdateRefs, input.BaseHash)
			case "update-new-ref":
				input.UpdateRefs = "refs/heads/adopted"
			case "target-omission":
				// target contains a file deliberately omitted by the resolution.
				runGit(t, repo, "git", "checkout", "--detach", input.TargetBaseHash)
				require.NoError(t, os.WriteFile(filepath.Join(repo, "target-only"), []byte("omitted"), 0600))
				runGit(t, repo, "git", "add", ".")
				runGit(t, repo, "git", "commit", "-m", "new target")
				input.TargetBaseHash = gitRevParse(t, repo, "HEAD")
				runGit(t, repo, "git", "checkout", "--detach", input.PersistHash)
			}
			input.PersistHash = gitRevParse(t, repo, "HEAD")
			input.Resolution.TreeHash = gitRevParse(t, repo, "HEAD^{tree}")
			out, err := NewThinpackRebaseActivity().Execute(adoptionDeps(input), context.Background(), input)
			require.NoError(t, err)
			require.Equal(t, input.Resolution.TreeHash, gitRevParse(t, repo, "HEAD^{tree}"))
			if mode == "equal-tree" {
				require.Equal(t, input.TargetBaseHash, out.NewPersistHash)
			} else {
				require.Equal(t, input.TargetBaseHash, gitRevParse(t, repo, "HEAD^"))
			}
			if mode == "configured-author" {
				require.Equal(t, "Worker <worker@example.com>", strings.TrimSpace(runGitOutput(t, repo, "git", "show", "-s", "--format=%an <%ae>", "HEAD")))
			}
			if input.UpdateRefs != "" {
				require.Equal(t, input.UpdateRefs, out.UpdatedRef)
				require.Equal(t, out.NewPersistHash, gitRevParse(t, repo, input.UpdateRefs))
			}
			if mode == "target-omission" {
				require.NoFileExists(t, filepath.Join(repo, "target-only"))
			}
		})
	}
}

func TestAdoptionRejectedExpectationsBeforeMutation(t *testing.T) {
	for _, mode := range []string{"stale-base", "stale-candidate", "stale-tree", "missing-target", "tree-as-target", "tag-as-target", "short-hash", "missing-persist", "unrelated-target", "unrelated-candidate", "floor", "tracked", "staged", "untracked", "unmerged", "MERGE_HEAD", "rebase-apply", "rebase-merge", "CHERRY_PICK_HEAD", "REVERT_HEAD", "sequencer", "linked-sequencer", "no-authority", "no-setter", "separate-checkout"} {
		t.Run(mode, func(t *testing.T) {
			input := adoptionFixture(t)
			deps := adoptionDeps(input)
			repo := input.RepoPath
			switch mode {
			case "stale-base":
				input.BaseHash = gitRevParse(t, repo, input.BaseHash+"^") // valid ancestor, wrong authority
			case "stale-candidate":
				input.PersistHash = input.BaseHash
			case "stale-tree":
				input.Resolution.TreeHash = gitRevParse(t, repo, input.BaseHash+"^{tree}")
			case "missing-target":
				input.TargetBaseHash = strings.Repeat("a", 40)
			case "tree-as-target":
				input.TargetBaseHash = input.Resolution.TreeHash
			case "tag-as-target":
				runGit(t, repo, "git", "tag", "-a", "target-tag", input.TargetBaseHash, "-m", "tag")
				input.TargetBaseHash = gitRevParse(t, repo, "target-tag")
			case "short-hash":
				input.BaseHash = input.BaseHash[:7]
			case "missing-persist":
				input.PersistHash = ""
			case "unrelated-target", "unrelated-candidate":
				cmd := exec.Command("git", "commit-tree", input.Resolution.TreeHash)
				cmd.Dir = repo
				cmd.Stdin = strings.NewReader("unrelated\n")
				b, err := cmd.Output()
				require.NoError(t, err)
				orphan := strings.TrimSpace(string(b))
				if mode == "unrelated-target" {
					input.TargetBaseHash = orphan
				} else {
					runGit(t, repo, "git", "reset", "--hard", orphan)
					input.PersistHash = orphan
					deps = adoptionDeps(input)
				}
			case "floor":
				deps = ops.NewOpDependenciesBuilder().WithWorktreePath(repo).WithGitContext(ops.GitExecutionContext{
					ResolvedBaseHash: input.BaseHash, PersistHash: input.PersistHash,
					Workspace: &contextual.WorkspaceContext{InitialHash: input.PersistHash, Initial: input.PersistHash},
				}).Build()
			case "tracked", "staged", "untracked":
				name := "file"
				if mode == "untracked" {
					name = "untracked"
				}
				require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte("keep dirty"), 0600))
				if mode == "staged" {
					runGit(t, repo, "git", "add", ".")
				}
			case "unmerged":
				cmd := exec.Command("git", "read-tree", "-m", input.BaseHash, input.PersistHash, input.TargetBaseHash)
				cmd.Dir = repo
				require.NoError(t, cmd.Run())
			case "no-authority":
				deps = ops.NewOpDependenciesBuilder().WithWorktreePath(repo).Build()
			case "no-setter":
				deps = struct{ ops.OpDependencies }{deps}
			case "separate-checkout":
				deps = ops.NewOpDependenciesBuilder().WithWorktreePath(t.TempDir()).Build()
			default:
				state := mode
				if mode == "linked-sequencer" {
					linked := filepath.Join(t.TempDir(), "linked")
					runGit(t, repo, "git", "worktree", "add", "--detach", linked, input.PersistHash)
					repo, input.RepoPath, state = linked, linked, "sequencer"
					deps = adoptionDeps(input)
				}
				path := strings.TrimSpace(runGitOutput(t, repo, "git", "rev-parse", "--git-path", state))
				if !filepath.IsAbs(path) {
					path = filepath.Join(repo, path)
				}
				if strings.Contains(state, "rebase") || state == "sequencer" {
					require.NoError(t, os.MkdirAll(path, 0755))
				} else {
					require.NoError(t, os.WriteFile(path, []byte(input.TargetBaseHash+"\n"), 0600))
				}
			}
			head := gitRevParse(t, repo, "HEAD")
			indexPath := strings.TrimSpace(runGitOutput(t, repo, "git", "rev-parse", "--git-path", "index"))
			if !filepath.IsAbs(indexPath) {
				indexPath = filepath.Join(repo, indexPath)
			}
			index, err := os.ReadFile(indexPath)
			require.NoError(t, err)
			refs := runGitOutput(t, repo, "git", "show-ref")
			_, err = NewThinpackRebaseActivity().Execute(deps, context.Background(), input)
			require.Error(t, err)
			var failure *Failure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, FailedCode, failure.Code)
			require.Equal(t, head, gitRevParse(t, repo, "HEAD"))
			require.Equal(t, refs, runGitOutput(t, repo, "git", "show-ref"))
			freshIndex, err := os.ReadFile(indexPath)
			require.NoError(t, err)
			// Git status may refresh stat information; staged entries must agree.
			require.Equal(t, index, freshIndex)
			if source, ok := deps.(interface{ WorkspaceBase() string }); ok {
				require.Empty(t, source.WorkspaceBase())
			}
		})
	}
}

func TestAdoptionConstructionAndInstallationFailures(t *testing.T) {
	for _, mode := range []string{"construct", "install", "ref", "cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			input := adoptionFixture(t)
			deps := adoptionDeps(input)
			repo := input.RepoPath
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			switch mode {
			case "construct":
				// Invalid committer identity makes commit-tree fail before checkout.
				input.GitAuthor = "Worker <>"
				runGit(t, repo, "git", "config", "user.email", "")
				runGit(t, repo, "git", "config", "user.name", "")
			case "install":
				require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "index.lock"), nil, 0600))
			case "ref":
				input.UpdateRefs = "refs/heads/main/invalid-child" // main already exists
			case "cancel":
				cancel(context.Canceled)
			case "timeout":
				cancel(context.DeadlineExceeded)
			}
			_, err := NewThinpackRebaseActivity().Execute(deps, ctx, input)
			var failure *Failure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, FailedCode, failure.Code)
			require.Equal(t, input.PersistHash, gitRevParse(t, repo, "HEAD"))
			require.Empty(t, deps.(interface{ WorkspaceBase() string }).WorkspaceBase())
			if mode == "install" {
				require.False(t, failure.Details.WorkspaceRestored)
				require.NotEmpty(t, failure.Details.CleanupError)
			}
			if mode == "ref" {
				require.True(t, failure.Details.WorkspaceRestored)
			}
			if mode == "cancel" || mode == "timeout" {
				require.ErrorIs(t, err, context.Cause(ctx))
				require.False(t, jobdb.IsAppError(err))
			}
		})
	}
}

// Assert raw commit message preservation independently of pretty-printing.
func TestAdoptionRawMessagePreservation(t *testing.T) {
	for _, original := range []string{"Message with trailing whitespace  \n\nbody\n\n", "Message without final newline"} {
		t.Run(original, func(t *testing.T) {
			input := adoptionFixture(t)
			raw := runGitOutput(t, input.RepoPath, "git", "cat-file", "commit", input.PersistHash)
			headers, _, ok := strings.Cut(raw, "\n\n")
			require.True(t, ok)
			cmd := exec.Command("git", "hash-object", "-w", "-t", "commit", "--stdin")
			cmd.Dir, cmd.Stdin = input.RepoPath, bytes.NewReader([]byte(headers+"\n\n"+original))
			hash, err := cmd.Output()
			require.NoError(t, err)
			input.PersistHash = strings.TrimSpace(string(hash))
			runGit(t, input.RepoPath, "git", "reset", "--hard", input.PersistHash)
			_, err = NewThinpackRebaseActivity().Execute(adoptionDeps(input), context.Background(), input)
			require.NoError(t, err)
			raw = runGitOutput(t, input.RepoPath, "git", "cat-file", "commit", "HEAD")
			_, message, ok := strings.Cut(raw, "\n\n")
			require.True(t, ok)
			require.Equal(t, original, message)
		})
	}
}

func TestAdoptionShallowAncestry(t *testing.T) {
	for _, remoteAvailable := range []bool{true, false} {
		t.Run(map[bool]string{true: "bounded-fetch", false: "unprovable"}[remoteAvailable], func(t *testing.T) {
			input := adoptionFixture(t)
			source := input.RepoPath
			clone := filepath.Join(t.TempDir(), "clone")
			runGit(t, filepath.Dir(clone), "git", "clone", "--depth", "1", "file://"+source, clone)
			for _, hash := range []string{input.BaseHash, input.TargetBaseHash} {
				runGit(t, clone, "git", "fetch", "--depth", "1", "origin", hash)
			}
			configureAuthor(t, clone, "Worker", "worker@example.com")
			if !remoteAvailable {
				runGit(t, clone, "git", "remote", "remove", "origin")
			}
			input.RepoPath = clone
			deps := adoptionDeps(input)
			out, err := NewThinpackRebaseActivity().Execute(deps, context.Background(), input)
			if remoteAvailable {
				require.NoError(t, err)
				require.Equal(t, input.TargetBaseHash, out.NewBaseHash)
				require.Equal(t, input.Resolution.TreeHash, gitRevParse(t, clone, "HEAD^{tree}"))
				require.Equal(t, "true", strings.TrimSpace(runGitOutput(t, clone, "git", "rev-parse", "--is-shallow-repository")))
			} else {
				require.ErrorContains(t, err, "incomplete shallow history")
				require.Equal(t, input.PersistHash, gitRevParse(t, clone, "HEAD"))
				require.Empty(t, deps.(interface{ WorkspaceBase() string }).WorkspaceBase())
			}
		})
	}
}

func TestReplaySelectorsRetainAbbreviationsWithoutResolution(t *testing.T) {
	for _, op := range []ops.RegisterableOp{GetOp(), GetRebaseOp()} {
		t.Run(op.GetMetadata().Type, func(t *testing.T) {
			input := adoptionFixture(t)
			result, err := op.TaskChain()[0].Invoke(adoptionDeps(input), context.Background(), map[string]any{
				"repo_path": input.RepoPath, "base_hash": input.BaseHash[:7], "persist_hash": input.PersistHash[:7], "target_base_hash": input.BaseHash[:7],
			})
			require.NoError(t, err)
			require.Equal(t, input.BaseHash, result["new_base_hash"])
			require.Equal(t, input.PersistHash, result["new_persist_hash"])
		})
	}
}

func TestAdoptionRestoresExistingRefAfterInstallationFailure(t *testing.T) {
	input := adoptionFixture(t)
	input.UpdateRefs = "refs/heads/adopted"
	runGit(t, input.RepoPath, "git", "update-ref", input.UpdateRefs, input.BaseHash)
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	shim := t.TempDir()
	t.Setenv("C2J_ADOPT_REAL_GIT", realGit)
	t.Setenv("C2J_ADOPT_REF", input.UpdateRefs)
	t.Setenv("C2J_ADOPT_REF_MARKER", filepath.Join(shim, "ref-installed"))
	script := `#!/bin/sh
if test "$1" = update-ref && test "$2" = "$C2J_ADOPT_REF"; then
 "$C2J_ADOPT_REAL_GIT" "$@" || exit $?
 touch "$C2J_ADOPT_REF_MARKER"
 exit 0
fi
if test "$1" = rev-parse && test "$2" = "$C2J_ADOPT_REF" && test -f "$C2J_ADOPT_REF_MARKER"; then
 rm "$C2J_ADOPT_REF_MARKER"
 echo 'injected failure after installing candidate and ref' >&2
 exit 73
fi
exec "$C2J_ADOPT_REAL_GIT" "$@"
`
	require.NoError(t, os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0755))
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	deps := adoptionDeps(input)
	_, err = NewThinpackRebaseActivity().Execute(deps, context.Background(), input)
	var failure *Failure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, FailedCode, failure.Code)
	require.True(t, failure.Details.WorkspaceRestored)
	require.Equal(t, input.PersistHash, gitRevParse(t, input.RepoPath, "HEAD"))
	// Inspect with the real Git executable; the fault shim deliberately rejects
	// the verification lookup after every update of this ref.
	require.Equal(t, input.BaseHash, strings.TrimSpace(runGitOutput(t, input.RepoPath, realGit, "rev-parse", input.UpdateRefs)))
	require.Empty(t, deps.(interface{ WorkspaceBase() string }).WorkspaceBase())
}

func TestAdoptionFailureKeepsInfrastructureClassification(t *testing.T) {
	for _, cause := range []error{
		&jobdb.SystemError{Payload: jobdb.SystemErrorPayload{Code: "transport", Message: "injected infrastructure failure"}},
		&jobdb.TimeoutError{Payload: jobdb.TimeoutPayload{Kind: "task", Scope: "invocation", Message: "injected timeout"}},
	} {
		err := captureFailure(context.Background(), cause, workspaceSnapshot{}, "", "construct_resolution", false, false)
		var failure *Failure
		require.ErrorAs(t, err, &failure)
		require.Equal(t, FailedCode, failure.Code)
		require.ErrorIs(t, err, cause)
		require.False(t, jobdb.IsAppError(err))
	}
}
