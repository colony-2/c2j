package squashrebasemerge

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/stretchr/testify/require"
)

func TestMergeAdvancementWithRealHTTPTransport(t *testing.T) {
	for _, mode := range []string{"advance", "authorization", "transport", "timeout", "cancel", "remote hook", "lost response"} {
		t.Run(mode, func(t *testing.T) {
			f := newAdvancementFixture(t)
			runGit(t, f.remote, "git", "config", "http.receivepack", "true")
			execPath := strings.TrimSpace(runGitOutput(t, f.repo, "git", "--exec-path"))
			backendPath := filepath.Join(execPath, "git-http-backend")
			if _, err := os.Stat(backendPath); os.IsNotExist(err) {
				t.Skip("Git smart HTTP backend not installed")
			}
			backend := &cgi.Handler{Path: backendPath, Root: "/", Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(f.remote), "GIT_HTTP_EXPORT_ALL=1"}}
			if mode == "remote hook" {
				require.NoError(t, os.WriteFile(filepath.Join(f.remote, "hooks/pre-receive"), []byte("#!/bin/sh\necho 'protected branch' >&2\nexit 1\n"), 0700))
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			var moved atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				discovery := r.URL.Query().Get("service") == "git-receive-pack"
				posting := r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-receive-pack")
				if mode == "lost response" && posting {
					response := httptest.NewRecorder()
					backend.ServeHTTP(response, r)
					// Publication succeeded; another writer then extends it.
					tree, err := common.GetCommitHash(context.Background(), f.remote, "main^{tree}")
					if err != nil {
						t.Error(err)
						http.Error(w, "fixture failed", 500)
						return
					}
					out, err := common.ExecuteGitCommand(context.Background(), f.remote, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit-tree", tree, "-p", f.candidate, "-m", "after publication")
					if err != nil {
						t.Error(err)
						http.Error(w, "fixture failed", 500)
						return
					}
					_, err = common.ExecuteGitCommand(context.Background(), f.remote, "update-ref", "refs/heads/main", strings.TrimSpace(string(out)))
					if err != nil {
						t.Error(err)
					}
					moved.Store(true)
					http.Error(w, "lost response", 503)
					return
				}
				advance := discovery && mode != "remote hook" && mode != "lost response" || posting && mode == "remote hook"
				if advance && moved.CompareAndSwap(false, true) {
					_, err := common.ExecuteGitCommand(context.Background(), f.remote, "update-ref", "refs/heads/main", f.upstream)
					if err != nil {
						t.Error(err)
						http.Error(w, "fixture failed", 500)
						return
					}
				}
				if discovery {
					switch mode {
					case "authorization":
						http.Error(w, "denied", 403)
						return
					case "transport":
						http.Error(w, "unavailable", 503)
						return
					case "timeout":
						cancel(context.DeadlineExceeded)
						http.Error(w, "timeout", 504)
						return
					case "cancel":
						cancel(context.Canceled)
						http.Error(w, "cancelled", 499)
						return
					}
				}
				backend.ServeHTTP(w, r)
			}))
			defer server.Close()
			input := f.input()
			input.UpstreamRepo = server.URL + "/remote.git"
			out, err := merge(ctx, input, false)
			require.True(t, moved.Load(), "fixture must move the branch during push")
			switch mode {
			case "advance":
				requireAdvancement(t, err, f, "push")
			case "lost response":
				require.NoError(t, err)
				require.Equal(t, f.candidate, out.MergedHash)
				require.Equal(t, f.candidate, revParse(t, f.remote, "main^"))
			default:
				require.Error(t, err)
				require.False(t, isMergeAdvancement(err), "%v", err)
				if mode == "timeout" {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				}
				if mode == "cancel" {
					require.ErrorIs(t, err, context.Canceled)
				}
				require.Equal(t, f.upstream, revParse(t, f.remote, "main"))
			}
			require.Equal(t, f.candidate, revParse(t, f.repo, "HEAD"))
		})
	}
}
