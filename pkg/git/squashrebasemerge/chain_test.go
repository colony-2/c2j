package squashrebasemerge

import (
	"context"
	"path/filepath"
	"strings"
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

func TestCombinedPublicationEvidence(t *testing.T) {
	for _, noChanges := range []bool{false, true} {
		t.Run(map[bool]string{false: "rebased", true: "no_changes"}[noChanges], func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			remote, repo := filepath.Join(root, "remote.git"), filepath.Join(root, "work")
			initBareRemote(t, remote)
			runGit(t, root, "git", "clone", remote, repo)
			configureAuthor(t, repo, "Test", "test@example.com")
			writeFile(t, repo, "base", "base")
			runGit(t, repo, "git", "add", ".")
			runGit(t, repo, "git", "commit", "-m", "base")
			base := revParse(t, repo, "HEAD")
			runGit(t, repo, "git", "push", "origin", "HEAD:main")
			if !noChanges {
				writeFile(t, repo, "feature", "feature")
				runGit(t, repo, "git", "add", ".")
				runGit(t, repo, "git", "commit", "-m", "actual subject")
				feature := revParse(t, repo, "HEAD")
				runGit(t, repo, "git", "reset", "--hard", base)
				writeFile(t, repo, "upstream", "upstream")
				runGit(t, repo, "git", "add", ".")
				runGit(t, repo, "git", "commit", "-m", "upstream")
				runGit(t, repo, "git", "push", "origin", "HEAD:main")
				runGit(t, repo, "git", "reset", "--hard", feature)
			}
			// Rebased and no-change candidates must report their actual commit subject.
			rebase := !noChanges
			prepared, err := preparePublication(nil, ctx, SquashRebaseMergeInput{RepoPath: repo, UpstreamRepo: remote, UpstreamBranch: "main", Rebase: &rebase, CommitMessage: "requested override"})
			require.NoError(t, err)
			state, err := rebasePublication(nil, ctx, prepared)
			require.NoError(t, err)
			state.Input.CommitMessage = "later ignored request"
			out, err := mergePublication(nil, ctx, state)
			require.NoError(t, err)
			want := PublicationPublished
			if noChanges {
				want = PublicationNoChanges
			}
			require.Equal(t, want, out.PublicationDisposition)
			require.Equal(t, state.Candidate, out.CandidateSHA)
			require.Equal(t, "refs/heads/main", out.PublicationBranch)
			actual := strings.TrimSuffix(runGitOutput(t, repo, "git", "show", "-s", "--format=%s", state.Candidate), "\n")
			require.Equal(t, actual, out.CandidateSubject)
			require.NotEqual(t, state.Input.CommitMessage, out.CandidateSubject)
			if noChanges {
				require.Equal(t, "base", out.CandidateSubject)
				require.NotEqual(t, "requested override", out.CandidateSubject)
			}
		})
	}
}
