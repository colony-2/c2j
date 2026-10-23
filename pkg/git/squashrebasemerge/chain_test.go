package squashrebasemerge

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/stretchr/testify/require"
)

func TestPublicationPinsTargetAndRejectsChangedCandidate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote, repo := filepath.Join(root, "remote.git"), filepath.Join(root, "work")
	initBareRemote(t, remote)
	runGit(t, root, "git", "clone", remote, repo)
	configureAuthor(t, repo, "Test", "test@example.com")
	writeFile(t, repo, "base", "base")
	runGit(t, repo, "git", "add", ".")
	runGit(t, repo, "git", "commit", "-m", "initial")
	initial := revParse(t, repo, "HEAD")
	runGit(t, repo, "git", "push", "origin", "HEAD:main")
	writeFile(t, repo, "feature", "feature")
	runGit(t, repo, "git", "add", ".")
	runGit(t, repo, "git", "commit", "-m", "feature")
	feature := revParse(t, repo, "HEAD")
	runGit(t, repo, "git", "reset", "--hard", initial)
	writeFile(t, repo, "upstream", "upstream")
	runGit(t, repo, "git", "add", ".")
	runGit(t, repo, "git", "commit", "-m", "upstream")
	target := revParse(t, repo, "HEAD")
	runGit(t, repo, "git", "push", "origin", "HEAD:main")
	runGit(t, repo, "git", "reset", "--hard", feature)
	prepared, err := preparePublication(nil, ctx, SquashRebaseMergeInput{RepoPath: repo, UpstreamRepo: remote, UpstreamBranch: "main"})
	require.NoError(t, err)
	require.Equal(t, target, prepared.TargetHash)
	squashed := prepared.Candidate
	// The destination changes after preparation, before the rebase task.
	runGit(t, repo, "git", "reset", "--hard", target)
	writeFile(t, repo, "later", "later")
	runGit(t, repo, "git", "add", ".")
	runGit(t, repo, "git", "commit", "-m", "later")
	later := revParse(t, repo, "HEAD")
	runGit(t, repo, "git", "push", "origin", "HEAD:main")
	runGit(t, repo, "git", "reset", "--hard", squashed)
	rebased, err := rebasePublication(nil, ctx, prepared)
	require.NoError(t, err)
	require.Equal(t, target, revParse(t, repo, "HEAD^"))
	_, err = mergePublication(nil, ctx, rebased)
	require.ErrorIs(t, err, common.ErrNotFastForward)
	require.Equal(t, later, revParse(t, remote, "main"))
	require.Equal(t, rebased.Candidate, revParse(t, repo, "HEAD"))
	// Even a different locally fast-forwardable commit must not substitute for
	// the saved candidate on a retry.
	runGit(t, repo, "git", "reset", "--hard", later)
	_, err = mergePublication(nil, ctx, rebased)
	require.ErrorContains(t, err, "does not match saved publication candidate")
	require.Equal(t, later, revParse(t, remote, "main"))
}
