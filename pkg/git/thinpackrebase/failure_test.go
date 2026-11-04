package thinpackrebase

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func conflictFixture(t *testing.T, path, mode string) (string, string, string, string) {
	t.Helper()
	repo := t.TempDir()
	initRepo(t, repo)
	content := "base\n"
	if mode == "rename" {
		content = strings.Repeat("unchanged context line\n", 30)
	}
	if mode == "binary" {
		content = "base\x00data"
	}
	require.NoError(t, os.WriteFile(filepath.Join(repo, path), []byte(content), 0600))
	runGit(t, repo, "git", "add", ".")
	runGit(t, repo, "git", "commit", "-m", "base")
	base := gitRevParse(t, repo, "HEAD")
	if mode == "delete" {
		runGit(t, repo, "git", "rm", "--", path)
	} else {
		require.NoError(t, os.WriteFile(filepath.Join(repo, path), []byte("upstream\n"+content), 0600))
		if mode == "rename" {
			runGit(t, repo, "git", "mv", "--", path, "renamed\nfile")
		}
		runGit(t, repo, "git", "add", ".")
	}
	runGit(t, repo, "git", "commit", "-m", "upstream")
	target := gitRevParse(t, repo, "HEAD")
	runGit(t, repo, "git", "checkout", "--detach", base)
	// Exercise abort after a prior commit has already replayed successfully.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "unrelated"), []byte("candidate"), 0600))
	runGit(t, repo, "git", "add", ".")
	runGit(t, repo, "git", "commit", "-m", "first local change")
	require.NoError(t, os.WriteFile(filepath.Join(repo, path), []byte("local\n"+content), 0600))
	runGit(t, repo, "git", "add", ".")
	runGit(t, repo, "git", "commit", "-m", "conflicting local change")
	return repo, base, target, gitRevParse(t, repo, "HEAD")
}

func TestStructuredConflictPreservesCandidateAndEvidence(t *testing.T) {
	for _, tc := range []struct{ path, mode, status string }{
		{"ordinary.txt", "text", "UU"}, {"space and\nnewline.txt", "text", "UU"},
		{"binary.bin", "binary", "UU"}, {"deleted.txt", "delete", "DU"},
		{"original", "rename", "UU"},
	} {
		t.Run(tc.mode+tc.path, func(t *testing.T) {
			repo, base, target, candidate := conflictFixture(t, tc.path, tc.mode)
			out, err := Run(celltest.Context(context.Background()), ThinpackRebaseInput{RepoPath: repo, BaseHash: base, TargetBaseHash: target})
			require.Nil(t, out)
			var f *Failure
			require.ErrorAs(t, err, &f)
			require.Equal(t, ConflictCode, f.Code)
			conflictPath := tc.path
			if tc.mode == "rename" {
				conflictPath = "renamed\nfile"
			}
			require.Equal(t, []Conflict{{Path: conflictPath, Status: tc.status}}, f.Details.Conflicts)
			require.Equal(t, base, *f.Details.BaseHash)
			require.Equal(t, candidate, *f.Details.CandidateHash)
			require.Equal(t, target, *f.Details.TargetBaseHash)
			require.True(t, f.Details.WorkspaceRestored)
			require.Empty(t, f.Details.CleanupError)
			require.NotEmpty(t, f.evidence["git-output.txt"])
			require.Contains(t, string(f.evidence["conflict-index.z"]), conflictPath)
			require.NotEmpty(t, f.evidence["git-stderr.txt"])
			require.NotEmpty(t, f.evidence["replayed-commit.patch"])
			require.Equal(t, candidate, gitRevParse(t, repo, "HEAD"))
			require.Empty(t, runGitOutput(t, repo, "git", "status", "--porcelain"))
			var app jobdb.AppError
			require.ErrorAs(t, err, &app)
			require.Equal(t, ConflictCode, app.Payload.Attrs["code"])
		})
	}
}

func TestNonConflictFailuresKeepClassification(t *testing.T) {
	for _, mode := range []string{"authentication", "cancel", "timeout", "ancestry", "dirty"} {
		t.Run(mode, func(t *testing.T) {
			repo, base, target, candidate := conflictFixture(t, "file", "text")
			ctx, cancel := context.WithCancelCause(celltest.Context(context.Background()))
			defer cancel(nil)
			input := ThinpackRebaseInput{RepoPath: repo, BaseHash: base, TargetBaseHash: target}
			switch mode {
			case "authentication", "cancel", "timeout":
				srv := httptest.NewServer(celltest.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if mode == "cancel" {
						cancel(context.Canceled)
					}
					if mode == "timeout" {
						cancel(context.DeadlineExceeded)
					}
					http.Error(w, "denied", http.StatusForbidden)
				})))
				defer srv.Close()
				runGit(t, repo, "git", "remote", "add", "origin", srv.URL+"/repo.git")
				input.TargetBaseHash = strings.Repeat("a", 40)
			case "ancestry":
				input.TargetBaseHash = gitRevParse(t, repo, base+"^")
			case "dirty":
				require.NoError(t, os.WriteFile(filepath.Join(repo, "dirty"), []byte("keep"), 0600))
			}
			_, err := Run(ctx, input)
			var f *Failure
			require.ErrorAs(t, err, &f)
			require.Equal(t, FailedCode, f.Code)
			require.Equal(t, candidate, gitRevParse(t, repo, "HEAD"))
			if mode == "cancel" || mode == "timeout" {
				require.ErrorIs(t, err, context.Cause(ctx))
				require.False(t, jobdb.IsAppError(err))
			}
		})
	}
}

func TestCleanupFailureDoesNotHideOriginalConflict(t *testing.T) {
	repo, base, target, candidate := conflictFixture(t, "file", "text")
	// Leave a real conflicted index, then make Git unable to acquire its lock.
	err := rebaseOntoTarget(celltest.Context(context.Background()), workspaceSnapshot{RepoPath: repo, BaseHash: base, PersistHash: candidate}, target, nil)
	require.Error(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "index.lock"), nil, 0600))
	f := captureFailure(celltest.Context(context.Background()), err, workspaceSnapshot{RepoPath: repo, BaseHash: base, PersistHash: candidate}, target, "replay", true, true).(*Failure)
	require.Equal(t, ConflictCode, f.Code)
	require.False(t, f.Details.WorkspaceRestored)
	require.NotEmpty(t, f.Details.CleanupError)
	require.ErrorIs(t, f, err)
	require.NoError(t, os.Remove(filepath.Join(repo, ".git", "index.lock")))
	require.NoError(t, abortRebase(celltest.Context(context.Background()), repo))
}

func TestCancellationAbortsWithIndependentContext(t *testing.T) {
	repo, base, target, candidate := conflictFixture(t, "file", "text")
	err := rebaseOntoTarget(celltest.Context(context.Background()), workspaceSnapshot{RepoPath: repo, BaseHash: base, PersistHash: candidate}, target, nil)
	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), time.Minute)
	cancel()
	f := captureFailure(ctx, errors.Join(err, ctx.Err()), workspaceSnapshot{RepoPath: repo, BaseHash: base, PersistHash: candidate}, target, "replay", true, true).(*Failure)
	require.Equal(t, FailedCode, f.Code)
	require.ErrorIs(t, f, context.Canceled)
	require.False(t, jobdb.IsAppError(f))
	require.True(t, f.Details.WorkspaceRestored)
	require.Equal(t, candidate, gitRevParse(t, repo, "HEAD"))
}

func TestRebaseCanceledBeforeValidationKeepsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(celltest.Context(context.Background()))
	cancel()
	_, err := Run(ctx, ThinpackRebaseInput{RepoPath: t.TempDir(), BaseHash: strings.Repeat("a", 40), TargetBaseHash: strings.Repeat("b", 40)})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, jobdb.IsAppError(err))
}
