package thinpackrebase

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunThinpackRebase_SharedUpstream(t *testing.T) {
	for _, mode := range []string{"later-shared-base", "shared-through-merge", "already-upstream", "target-already-in-candidate", "shallow", "shallow-merged-target", "shallow-merged-target-offline", "shallow-merged-target-no-pending"} {
		t.Run(mode, func(t *testing.T) {
			remote := filepath.Join(t.TempDir(), "remote")
			initRepo(t, remote)
			writeVersion := func(repo, value string) {
				require.NoError(t, os.WriteFile(filepath.Join(repo, "version"), []byte(value+"\n"), 0644))
				runGit(t, repo, "git", "add", "version")
				runGit(t, repo, "git", "commit", "-m", value)
			}
			writeVersion(remote, "old managed base")
			base := gitRevParse(t, remote, "HEAD")
			writeVersion(remote, "shared upstream")
			shared := gitRevParse(t, remote, "HEAD")
			workspace := filepath.Join(t.TempDir(), "workspace")
			if strings.HasPrefix(mode, "shallow") {
				runGit(t, remote, "git", "clone", "--depth", "1", "file://"+remote, workspace)
				runGit(t, workspace, "git", "fetch", "--depth", "1", "origin", base)
			} else {
				runGit(t, remote, "git", "clone", remote, workspace)
			}
			configureAuthor(t, workspace, "Dev", "dev@example.com")
			if mode == "shared-through-merge" {
				runGit(t, workspace, "git", "checkout", "-B", "local", base)
			}
			pending := mode != "already-upstream" && mode != "shallow-merged-target-no-pending"
			if pending {
				appendToFile(t, workspace, "local", "retain local work\n")
				runGit(t, workspace, "git", "add", "local")
				runGit(t, workspace, "git", "commit", "-m", "local work")
			}
			if mode == "shared-through-merge" {
				runGit(t, workspace, "git", "merge", "--no-ff", shared, "-m", "incorporate shared upstream")
			}
			candidate := gitRevParse(t, workspace, "HEAD")
			require.Equal(t, shared, strings.TrimSpace(runGitOutput(t, workspace, "git", "merge-base", "HEAD", shared)))
			if mode != "target-already-in-candidate" {
				if strings.HasPrefix(mode, "shallow-merged-target") {
					// The short parent proves base ancestry before the long parent
					// reveals the later shared commit. Both paths must be known.
					runGit(t, remote, "git", "checkout", "-b", "long-upstream")
					for i := 0; i < 8; i++ {
						appendToFile(t, remote, "side", "upstream\n")
						runGit(t, remote, "git", "add", "side")
						runGit(t, remote, "git", "commit", "-m", "upstream side")
					}
					runGit(t, remote, "git", "checkout", "-B", "main", base)
				}
				// Replaying base..candidate would try shared upstream again here
				// and conflict with this newer version, before local work runs.
				writeVersion(remote, "new pinned target")
				if strings.HasPrefix(mode, "shallow-merged-target") {
					runGit(t, remote, "git", "merge", "--no-ff", "-s", "ours", "long-upstream", "-m", "target with shared side history")
				}
			}
			target := gitRevParse(t, remote, "HEAD")
			if mode == "shallow-merged-target-offline" || mode == "shallow-merged-target-no-pending" {
				runGit(t, workspace, "git", "fetch", "--depth", "3", "origin", target)
				runGit(t, workspace, "git", "merge-base", "--is-ancestor", base, target)
				if !pending {
					runGit(t, workspace, "git", "reset", "--hard", target)
					candidate = target
				}
				runGit(t, workspace, "git", "remote", "remove", "origin")
			}
			out, err := Run(context.Background(), ThinpackRebaseInput{
				RepoPath: workspace, BaseHash: base, PersistHash: candidate,
				TargetBaseHash: target, GitAuthor: "Bot <bot@example.com>",
			})
			if mode == "shallow-merged-target-offline" {
				require.ErrorContains(t, err, "incomplete shallow history")
				require.Equal(t, candidate, gitRevParse(t, workspace, "HEAD"))
				require.NoError(t, requireCleanIntegration(context.Background(), workspace))
				return
			}
			require.NoError(t, err)
			require.Equal(t, target, out.NewBaseHash)
			require.Equal(t, target, out.TargetBaseHash)
			require.Equal(t, base, out.RebasedFrom.BaseHash, "report the recorded base, not the later shared commit")
			require.Equal(t, candidate, out.RebasedFrom.PersistHash)
			require.Equal(t, out.NewPersistHash, gitRevParse(t, workspace, "HEAD"))
			if pending {
				require.Equal(t, target, gitRevParse(t, workspace, "HEAD^"))
				require.Equal(t, "local work", strings.TrimSpace(runGitOutput(t, workspace, "git", "log", "-1", "--format=%s")))
				data, err := os.ReadFile(filepath.Join(workspace, "local"))
				require.NoError(t, err)
				require.Equal(t, "retain local work\n", string(data))
			} else {
				require.Equal(t, target, out.NewPersistHash, "all candidate commits are already upstream")
			}
			if mode == "target-already-in-candidate" {
				require.Equal(t, candidate, out.NewPersistHash, "retain an already integrated candidate")
			}
			version, err := os.ReadFile(filepath.Join(workspace, "version"))
			require.NoError(t, err)
			want := "new pinned target\n"
			if mode == "target-already-in-candidate" {
				want = "shared upstream\n"
			}
			require.Equal(t, want, string(version))
			require.NoError(t, requireCleanIntegration(context.Background(), workspace))
		})
	}
}
