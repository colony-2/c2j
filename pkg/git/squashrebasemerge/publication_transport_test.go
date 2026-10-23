package squashrebasemerge

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/stretchr/testify/require"
)

// This uses real Git smart HTTP with a test server implementing creation during
// receive-pack discovery, as gitvend does. It does not require a live forge.
func TestFirstPublicationCreatesRemoteOnlyDuringAuthorizedPush(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		t.Run(map[bool]string{true: "authorized", false: "denied"}[allowed], func(t *testing.T) {
			root := t.TempDir()
			remote := filepath.Join(root, "new.git")
			execPath := strings.TrimSpace(runGitOutput(t, root, "git", "--exec-path"))
			backendPath := filepath.Join(execPath, "git-http-backend")
			if _, err := os.Stat(backendPath); os.IsNotExist(err) {
				t.Skip("Git smart HTTP backend not installed")
			}
			backend := &cgi.Handler{Path: backendPath, Root: "/", Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
			var mu sync.Mutex
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests = append(requests, r.URL.String())
				mu.Unlock()
				if !allowed {
					http.Error(w, "creation denied", http.StatusForbidden)
					return
				}
				if r.URL.Query().Get("service") == "git-receive-pack" {
					if _, err := os.Stat(remote); os.IsNotExist(err) {
						if _, err = common.ExecuteGitCommand(r.Context(), root, "init", "--bare", remote); err != nil {
							http.Error(w, err.Error(), 500)
							return
						}
						if _, err = common.ExecuteGitCommand(r.Context(), remote, "config", "http.receivepack", "true"); err != nil {
							http.Error(w, err.Error(), 500)
							return
						}
					}
				}
				backend.ServeHTTP(w, r)
			}))
			defer server.Close()
			repo := t.TempDir()
			runGit(t, repo, "git", "init", "-b", "main")
			require.NoError(t, common.EnsureGitIdentity(context.Background(), repo))
			writeFile(t, repo, "PURPOSE.md", "approved purpose")
			runGit(t, repo, "git", "add", ".")
			runGit(t, repo, "git", "commit", "-m", "purpose")
			out, err := run(context.Background(), SquashRebaseMergeInput{RepoPath: repo, UpstreamRepo: server.URL + "/new.git", UpstreamBranch: "main", CommitMessage: "Create cell"}, true)
			mu.Lock()
			seen := append([]string(nil), requests...)
			mu.Unlock()
			require.NotEmpty(t, seen)
			require.Contains(t, seen[0], "service=git-receive-pack")
			if allowed {
				for _, request := range seen {
					require.NotContains(t, request, "upload-pack")
				}
			}
			if !allowed {
				require.ErrorContains(t, err, "403")
				_, err = os.Stat(remote)
				require.True(t, os.IsNotExist(err))
				return
			}
			require.NoError(t, err)
			require.Equal(t, out.MergedHash, revParse(t, remote, "refs/heads/main"))
		})
	}
}

func TestMergeLostPushResponseRestoresExactCandidate(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "initial"}[empty], func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			remote, repo := filepath.Join(root, "remote.git"), filepath.Join(root, "work")
			initBareRemote(t, remote)
			runGit(t, remote, "git", "config", "http.receivepack", "true")
			require.NoError(t, os.Mkdir(repo, 0700))
			runGit(t, repo, "git", "init", "-b", "main")
			require.NoError(t, common.EnsureGitIdentity(ctx, repo))
			writeFile(t, repo, "base", "base")
			runGit(t, repo, "git", "add", ".")
			runGit(t, repo, "git", "commit", "-m", "base")
			if !empty {
				runGit(t, repo, "git", "push", remote, "HEAD:main")
				writeFile(t, repo, "change", "prepared")
				runGit(t, repo, "git", "add", ".")
				runGit(t, repo, "git", "commit", "-m", "prepared")
			}
			candidate := revParse(t, repo, "HEAD")
			bundle := filepath.Join(root, "saved.bundle")
			runGit(t, repo, "git", "bundle", "create", bundle, "HEAD")
			execPath := strings.TrimSpace(runGitOutput(t, root, "git", "--exec-path"))
			backendPath := filepath.Join(execPath, "git-http-backend")
			if _, err := os.Stat(backendPath); os.IsNotExist(err) {
				t.Skip("Git smart HTTP backend not installed")
			}
			backend := &cgi.Handler{Path: backendPath, Root: "/", Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
			var lost atomic.Bool
			var receives atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if lost.Load() {
					// Also prevent immediate reconciliation, modeling an outage
					// that lasts until the next worker attempt.
					http.Error(w, "injected outage after accepting push", http.StatusServiceUnavailable)
					return
				}
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
					receives.Add(1)
					response := httptest.NewRecorder()
					backend.ServeHTTP(response, r) // The ref update completes here.
					lost.Store(true)
					http.Error(w, "injected lost push response", http.StatusServiceUnavailable)
					return
				}
				backend.ServeHTTP(w, r)
			}))
			defer server.Close()
			input := MergeInput{RepoPath: repo, UpstreamRepo: server.URL + "/remote.git", UpstreamBranch: "main"}
			_, err := merge(ctx, input, empty)
			require.Error(t, err)
			require.True(t, lost.Load())
			require.Equal(t, candidate, revParse(t, remote, "main"))
			require.NoError(t, os.RemoveAll(repo))
			restored := filepath.Join(root, "restored")
			runGit(t, root, "git", "clone", bundle, restored)
			runGit(t, restored, "git", "checkout", "--detach", candidate)
			input.RepoPath = restored
			lost.Store(false)
			out, err := merge(ctx, input, empty)
			require.NoError(t, err)
			require.Equal(t, candidate, out.MergedHash)
			require.Equal(t, candidate, revParse(t, remote, "main"))
			require.EqualValues(t, 1, receives.Load(), "recovery must not send another ref update")
		})
	}
}
