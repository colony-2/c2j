package squashrebasemerge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/stretchr/testify/require"
)

func TestSeparateSquashAndMerge(t *testing.T) {
	for _, empty := range []bool{false, true} {
		name := "existing"
		if empty {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			repo, remote := filepath.Join(root, "work"), filepath.Join(root, "remote.git")
			initBareRemote(t, remote)
			require.NoError(t, os.Mkdir(repo, 0700))
			runGit(t, repo, "git", "init", "-b", "main")
			require.NoError(t, common.EnsureGitIdentity(ctx, repo))
			base := ""
			if !empty {
				writeFile(t, repo, "base.txt", "base")
				runGit(t, repo, "git", "add", ".")
				runGit(t, repo, "git", "commit", "-m", "base")
				base = revParse(t, repo, "HEAD")
				runGit(t, repo, "git", "push", remote, "HEAD:refs/heads/main")
			}
			for _, f := range []string{"first.txt", "second.txt"} {
				writeFile(t, repo, f, f)
				runGit(t, repo, "git", "add", ".")
				runGit(t, repo, "git", "commit", "-m", f)
			}
			before := revParse(t, repo, "HEAD")
			tree := revParse(t, repo, "HEAD^{tree}")
			out, err := squash(ctx, SquashInput{RepoPath: repo, BaseHash: base, CommitMessage: "Prepared", Author: "Op Author <op@example.com>"}, empty)
			require.NoError(t, err)
			require.Equal(t, before, out.SquashedCommits.PersistHash)
			require.Equal(t, tree, revParse(t, repo, "HEAD^{tree}"))
			require.Equal(t, out.SquashedHash, revParse(t, repo, "HEAD"))
			require.Equal(t, "Op Author <op@example.com>", strings.TrimSpace(runGitOutput(t, repo, "git", "show", "-s", "--format=%an <%ae>", "HEAD")))
			if empty {
				require.Equal(t, "1", strings.TrimSpace(runGitOutput(t, repo, "git", "rev-list", "--count", "HEAD")))
				require.Empty(t, strings.TrimSpace(runGitOutput(t, repo, "git", "ls-remote", remote, "refs/heads/main")))
			} else {
				require.Equal(t, base, revParse(t, repo, "HEAD^"))
				require.Equal(t, base, revParse(t, remote, "refs/heads/main"))
			}
			input := MergeInput{RepoPath: repo, UpstreamRepo: remote, UpstreamBranch: "main"}
			for i := 0; i < 2; i++ { // Retry the exact candidate after a lost publication outcome.
				published, err := merge(ctx, input, empty)
				require.NoError(t, err)
				require.Equal(t, out.SquashedHash, published.MergedHash)
				require.Equal(t, out.SquashedHash, revParse(t, remote, "refs/heads/main"))
			}
			// A competing branch update must not be overwritten by a stale retry.
			writeFile(t, repo, "other.txt", "other")
			runGit(t, repo, "git", "add", ".")
			runGit(t, repo, "git", "commit", "-m", "competitor")
			other := revParse(t, repo, "HEAD")
			runGit(t, repo, "git", "push", remote, "HEAD:refs/heads/main")
			runGit(t, repo, "git", "reset", "--hard", out.SquashedHash)
			_, err = merge(ctx, input, empty)
			require.Error(t, err)
			require.Equal(t, other, revParse(t, remote, "refs/heads/main"))
			require.Equal(t, out.SquashedHash, revParse(t, repo, "HEAD"))
		})
	}
}

func TestSquashRejectsUnsafeInputs(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runGit(t, repo, "git", "init", "-b", "main")
	require.NoError(t, common.EnsureGitIdentity(ctx, repo))
	writeFile(t, repo, "base", "base")
	runGit(t, repo, "git", "add", ".")
	runGit(t, repo, "git", "commit", "-m", "base")
	head := revParse(t, repo, "HEAD")
	_, err := squash(ctx, SquashInput{RepoPath: repo}, false)
	require.ErrorContains(t, err, "base_hash is required")
	unchanged, err := squash(ctx, SquashInput{RepoPath: repo, BaseHash: head}, false)
	require.NoError(t, err)
	require.Equal(t, head, unchanged.SquashedHash)
	writeFile(t, repo, "base", "dirty")
	_, err = squash(ctx, SquashInput{RepoPath: repo, BaseHash: head}, false)
	require.ErrorContains(t, err, "workspace must be clean")
	require.Equal(t, head, revParse(t, repo, "HEAD"))
	content, err := os.ReadFile(filepath.Join(repo, "base"))
	require.NoError(t, err)
	require.Equal(t, "dirty", string(content))
}

func TestMergeRejectsDivergenceAndConcurrentUpdates(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprintf("concurrent=%t", concurrent), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			repo, remote := filepath.Join(root, "work"), filepath.Join(root, "remote.git")
			initBareRemote(t, remote)
			require.NoError(t, os.Mkdir(repo, 0700))
			runGit(t, repo, "git", "init", "-b", "main")
			require.NoError(t, common.EnsureGitIdentity(ctx, repo))
			writeFile(t, repo, "base", "base")
			runGit(t, repo, "git", "add", ".")
			runGit(t, repo, "git", "commit", "-m", "base")
			base := revParse(t, repo, "HEAD")
			runGit(t, repo, "git", "push", remote, "HEAD:refs/heads/main")
			writeFile(t, repo, "competitor", "competitor")
			runGit(t, repo, "git", "add", ".")
			runGit(t, repo, "git", "commit", "-m", "competing change")
			competitor := revParse(t, repo, "HEAD")
			runGit(t, repo, "git", "push", remote, "HEAD:refs/heads/other")
			runGit(t, repo, "git", "reset", "--hard", base)
			writeFile(t, repo, "candidate", "candidate")
			runGit(t, repo, "git", "add", ".")
			runGit(t, repo, "git", "commit", "-m", "prepared change")
			candidate := revParse(t, repo, "HEAD")
			if concurrent {
				hook := fmt.Sprintf("#!/bin/sh\ngit --git-dir='%s' update-ref refs/heads/main %s\n", remote, competitor)
				require.NoError(t, os.WriteFile(filepath.Join(repo, ".git/hooks/pre-push"), []byte(hook), 0700))
			} else {
				runGit(t, remote, "git", "update-ref", "refs/heads/main", competitor)
			}
			_, err := merge(ctx, MergeInput{RepoPath: repo, UpstreamRepo: remote, UpstreamBranch: "main"}, false)
			require.Error(t, err)
			require.Equal(t, candidate, revParse(t, repo, "HEAD"))
			require.Equal(t, competitor, revParse(t, remote, "refs/heads/main"))
		})
	}
}

func TestMergePreservesIndividualCommits(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo, remote := filepath.Join(root, "work"), filepath.Join(root, "remote.git")
	initBareRemote(t, remote)
	require.NoError(t, os.Mkdir(repo, 0700))
	runGit(t, repo, "git", "init", "-b", "main")
	require.NoError(t, common.EnsureGitIdentity(ctx, repo))
	var hashes []string
	for _, name := range []string{"base", "one", "two"} {
		writeFile(t, repo, name, name)
		runGit(t, repo, "git", "add", ".")
		runGit(t, repo, "git", "commit", "-m", name)
		hashes = append(hashes, revParse(t, repo, "HEAD"))
	}
	runGit(t, repo, "git", "push", remote, hashes[0]+":refs/heads/main")
	out, err := merge(ctx, MergeInput{RepoPath: repo, UpstreamRepo: remote, UpstreamBranch: "main"}, false)
	require.NoError(t, err)
	require.Equal(t, hashes[2], out.MergedHash)
	require.Equal(t, hashes[1], revParse(t, remote, "main^"))
	require.Equal(t, hashes[0], revParse(t, remote, "main^^"))
}
