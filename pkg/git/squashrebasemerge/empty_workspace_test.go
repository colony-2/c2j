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

func TestEmptyBasePublication(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, scenario := range []string{"empty remote", "other branch", "existing target", "concurrent target", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			remote := filepath.Join(root, "remote.git")
			initBareRemote(t, remote)
			repo := filepath.Join(root, "work")
			require.NoError(t, os.Mkdir(repo, 0700))
			runGit(t, repo, "git", "init", "-b", "main")
			require.NoError(t, common.EnsureGitIdentity(context.Background(), repo))
			for _, f := range []string{"PURPOSE.md", "more.txt"} {
				writeFile(t, repo, f, f)
				runGit(t, repo, "git", "add", ".")
				runGit(t, repo, "git", "commit", "-m", f)
			}
			before := revParse(t, repo, "HEAD")
			if scenario == "other branch" || scenario == "existing target" || scenario == "concurrent target" {
				runGit(t, repo, "git", "push", remote, "HEAD:refs/heads/other")
			}
			if scenario == "existing target" {
				runGit(t, remote, "git", "update-ref", "refs/heads/main", before)
			}
			if scenario == "concurrent target" {
				// pre-push runs after ref advertisement, before the server updates refs.
				hook := fmt.Sprintf("#!/bin/sh\ngit --git-dir='%s' update-ref refs/heads/main %s\n", remote, before)
				require.NoError(t, os.WriteFile(filepath.Join(repo, ".git/hooks/pre-push"), []byte(hook), 0700))
			}
			if scenario == "denied" {
				require.NoError(t, os.WriteFile(filepath.Join(remote, "hooks/pre-receive"), []byte("#!/bin/sh\necho publication-denied >&2\nexit 1\n"), 0700))
			}
			out, err := run(context.Background(), SquashRebaseMergeInput{RepoPath: repo, UpstreamRepo: remote, UpstreamBranch: "main", CommitMessage: "Approved purpose"}, true)
			if scenario == "existing target" || scenario == "concurrent target" || scenario == "denied" {
				require.ErrorContains(t, err, "destination must be absent")
				require.Nil(t, out)
				require.Equal(t, before, revParse(t, repo, "HEAD"))
				if scenario != "denied" {
					require.Equal(t, before, revParse(t, remote, "refs/heads/main"))
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, out.MergedHash, revParse(t, remote, "refs/heads/main"))
			require.Equal(t, "1", strings.TrimSpace(runGitOutput(t, remote, "git", "rev-list", "--count", "main")))
			require.Equal(t, "PURPOSE.md", strings.TrimSpace(runGitOutput(t, remote, "git", "show", "main:PURPOSE.md")))
			require.Equal(t, "Approved purpose", strings.TrimSpace(runGitOutput(t, remote, "git", "show", "-s", "--format=%s", "main")))
			require.Equal(t, common.DefaultGitAuthor+"|"+common.DefaultGitAuthor, strings.TrimSpace(runGitOutput(t, remote, "git", "show", "-s", "--format=%an <%ae>|%cn <%ce>", "main")))
			// Subsequent publication follows the ordinary existing-history path.
			runGit(t, repo, "git", "remote", "add", "origin", remote)
			writeFile(t, repo, "next.txt", "next")
			runGit(t, repo, "git", "add", ".")
			runGit(t, repo, "git", "commit", "-m", "next")
			next, err := Run(context.Background(), SquashRebaseMergeInput{RepoPath: repo, UpstreamRepo: remote, UpstreamBranch: "main"})
			require.NoError(t, err)
			require.Equal(t, out.MergedHash, revParse(t, repo, next.MergedHash+"^"))
		})
	}
}
