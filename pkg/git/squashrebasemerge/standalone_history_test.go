package squashrebasemerge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/shellcmd"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type squashHistoryFixture struct{ repo, source, base, published, candidate string }

// Both endpoints exist locally, but the intermediate published history does not.
// No unpublished commit is ever copied to the source.
func newSquashHistoryFixture(t *testing.T) squashHistoryFixture {
	t.Helper()
	root := t.TempDir()
	f := squashHistoryFixture{repo: filepath.Join(root, "work"), source: filepath.Join(root, "source")}
	require.NoError(t, os.Mkdir(f.source, 0700))
	runGit(t, f.source, "git", "init", "-b", "main")
	require.NoError(t, common.EnsureGitIdentity(context.Background(), f.source))
	for i := 0; i < 10; i++ {
		writeFile(t, f.source, "published", fmt.Sprintf("published %d\n", i))
		runGit(t, f.source, "git", "add", ".")
		runGit(t, f.source, "git", "commit", "-m", fmt.Sprintf("published %d", i))
		if i == 1 {
			f.base = revParse(t, f.source, "HEAD")
		}
	}
	f.published = revParse(t, f.source, "HEAD")
	runGit(t, root, "git", "clone", "--depth=1", "file://"+f.source, f.repo)
	runGit(t, f.repo, "git", "fetch", "--depth=1", "origin", f.base)
	require.NoError(t, common.EnsureGitIdentity(context.Background(), f.repo))
	require.NoError(t, os.WriteFile(filepath.Join(f.repo, "binary"), []byte{0, 255, 128, 10, 0}, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(f.repo, "executable"), []byte("#!/bin/sh\nexit 0\n"), 0755))
	require.NoError(t, os.Symlink("binary", filepath.Join(f.repo, "symbolic link")))
	runGit(t, f.repo, "git", "add", ".")
	runGit(t, f.repo, "git", "commit", "-m", "unpublished correction")
	f.candidate = revParse(t, f.repo, "HEAD")
	require.True(t, common.CommitExists(context.Background(), f.repo, f.base))
	ok, err := isAncestor(context.Background(), f.repo, f.base, f.candidate)
	require.NoError(t, err)
	require.False(t, ok, "fixture must hide ancestry")
	require.False(t, common.CommitExists(context.Background(), f.source, f.candidate))
	// Recovery must use BaseRepo, never this unusable origin.
	runGit(t, f.repo, "git", "remote", "set-url", "origin", filepath.Join(root, "wrong-source"))
	return f
}

func (f squashHistoryFixture) deps() ops.OpDependencies {
	return ops.NewOpDependenciesBuilder().WithWorktreePath(f.repo).WithGitContext(ops.GitExecutionContext{BaseRepo: f.source, ResolvedBaseHash: f.base, PersistHash: f.candidate}).Build()
}
func (f squashHistoryFixture) invoke(ctx context.Context, deps ops.OpDependencies, base string) (map[string]any, error) {
	return GetSquashOp().TaskChain()[0].Invoke(deps, ctx, map[string]any{"repo_path": f.repo, "base_hash": base, "author": "Owner <owner@example.test>", "commit_message": "Prepared correction"})
}

type squashWorkState struct{ head, tree, delta, index, status, worktree string }

func (f squashHistoryFixture) state(t *testing.T) squashWorkState {
	t.Helper()
	return squashWorkState{revParse(t, f.repo, "HEAD"), revParse(t, f.repo, "HEAD^{tree}"), runGitOutput(t, f.repo, "git", "diff", "--binary", "--full-index", f.published, "HEAD"), runGitOutput(t, f.repo, "git", "ls-files", "--stage", "-z"), runGitOutput(t, f.repo, "git", "status", "--porcelain=v1", "-z"), runGitOutput(t, f.repo, "git", "diff", "--binary", "--full-index", "HEAD")}
}

// Test-only observation/exception shim. Ordinary positive tests delegate every
// command to real Git. PATH changes prohibit parallel use of this fixture.
func recordSquashGit(t *testing.T, exceptional string) string {
	t.Helper()
	real, err := exec.LookPath("git")
	require.NoError(t, err)
	dir := t.TempDir()
	log := filepath.Join(dir, "commands")
	require.NoError(t, os.WriteFile(log, nil, 0600))
	script := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$*\" >> " + shellcmd.QuotePOSIX(log) + "\n" + exceptional + "\nexec " + shellcmd.QuotePOSIX(real) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}
func squashCommands(t *testing.T, log string) string {
	t.Helper()
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	return string(data)
}
func requireNoSquashMutation(t *testing.T, commands string) {
	t.Helper()
	for _, line := range strings.Split(commands, "\n") {
		require.False(t, strings.HasPrefix(line, "commit-tree ") || strings.HasPrefix(line, "reset "), line)
	}
}

func TestStandaloneSquashRecoversPublishedHistory(t *testing.T) {
	f := newSquashHistoryFixture(t)
	before := f.state(t)
	log := recordSquashGit(t, "")
	out, err := f.invoke(context.Background(), f.deps(), f.base)
	require.NoError(t, err)
	after := f.state(t)
	require.Equal(t, before.tree, after.tree)
	require.Equal(t, before.delta, after.delta)
	require.Equal(t, before.index, after.index)
	require.Equal(t, before.status, after.status)
	require.Equal(t, f.base, revParse(t, f.repo, "HEAD^"))
	require.Equal(t, after.head, out["squashed_hash"])
	require.Equal(t, map[string]any{"base_hash": f.base, "persist_hash": f.candidate}, out["squashed_commits"])
	require.Equal(t, f.base, f.deps().GitContext().ResolvedBaseHash)
	require.False(t, common.CommitExists(context.Background(), f.source, f.candidate))
	var depths []int
	for _, line := range strings.Split(squashCommands(t, log), "\n") {
		if !strings.HasPrefix(line, "fetch ") {
			continue
		}
		fields := strings.Fields(line)
		require.Equal(t, []string{"fetch", "--no-tags"}, fields[:2])
		depth, err := strconv.Atoi(strings.TrimPrefix(fields[2], "--depth="))
		require.NoError(t, err)
		depths = append(depths, depth)
		require.Equal(t, "--", fields[3])
		require.Equal(t, f.source, fields[4])
		require.NotEqual(t, f.candidate, fields[5])
		require.True(t, common.CommitExists(context.Background(), f.source, fields[5]))
	}
	require.NotEmpty(t, depths)
	for i, depth := range depths {
		require.Equal(t, 2<<i, depth)
	}
	require.Contains(t, runGitOutput(t, f.repo, "git", "ls-tree", "HEAD", "executable"), "100755")
	require.Equal(t, "Owner <owner@example.test>", strings.TrimSpace(runGitOutput(t, f.repo, "git", "show", "-s", "--format=%an <%ae>", "HEAD")))
	require.Equal(t, "Prepared correction", strings.TrimSpace(runGitOutput(t, f.repo, "git", "show", "-s", "--format=%s", "HEAD")))
}

func TestStandaloneSquashRejectsUnprovenHistory(t *testing.T) {
	for _, mode := range []string{"unrelated", "missing base", "missing input", "unavailable boundary", "incomplete source", "failed fetch", "nil deps", "unmanaged", "mismatch", "empty source", "path alias", "dirty", "invalid repository", "pre-cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := newSquashHistoryFixture(t)
			base := f.base
			deps := f.deps()
			ctx := context.Background()
			want := "ancestor of HEAD"
			noFetch := false
			switch mode {
			case "unrelated":
				base = strings.TrimSpace(runGitOutput(t, f.repo, "git", "commit-tree", revParse(t, f.repo, "HEAD^{tree}"), "-m", "unrelated root"))
			case "missing base":
				base = strings.Repeat("a", 40)
				want = "failed to get commit hash"
				noFetch = true
			case "missing input":
				base = ""
				want = "base_hash is required"
				noFetch = true
			case "unavailable boundary":
				// Published boundary is absent from this otherwise valid source.
				other := t.TempDir()
				runGit(t, other, "git", "init", "--bare")
				deps = ops.NewOpDependenciesBuilder().WithWorktreePath(f.repo).WithGitContext(ops.GitExecutionContext{BaseRepo: other}).Build()
				want = "fetch merge ancestry"
			case "incomplete source":
				source := filepath.Join(t.TempDir(), "shallow")
				runGit(t, filepath.Dir(source), "git", "clone", "--depth=1", "file://"+f.source, source)
				deps = ops.NewOpDependenciesBuilder().WithWorktreePath(f.repo).WithGitContext(ops.GitExecutionContext{BaseRepo: source}).Build()
				want = "incomplete shallow history"
			case "failed fetch":
				deps = ops.NewOpDependenciesBuilder().WithWorktreePath(f.repo).WithGitContext(ops.GitExecutionContext{BaseRepo: filepath.Join(t.TempDir(), "missing")}).Build()
				want = "fetch merge ancestry"
			case "nil deps":
				deps = nil
				noFetch = true
			case "unmanaged":
				deps = ops.NewOpDependenciesBuilder().Build()
				noFetch = true
			case "mismatch":
				deps = ops.NewOpDependenciesBuilder().WithWorktreePath(filepath.Dir(f.repo)).WithGitContext(ops.GitExecutionContext{BaseRepo: f.source}).Build()
				noFetch = true
			case "empty source":
				deps = ops.NewOpDependenciesBuilder().WithWorktreePath(f.repo).Build()
				noFetch = true
			case "path alias":
				deps = ops.NewOpDependenciesBuilder().WithWorktreePath(f.repo + string(os.PathSeparator) + ".").WithGitContext(ops.GitExecutionContext{BaseRepo: f.source}).Build()
				noFetch = true
			case "dirty":
				writeFile(t, f.repo, "binary", "dirty")
				want = "workspace must be clean"
				noFetch = true
			case "invalid repository":
				f.repo = t.TempDir()
				want = "not a git repository"
				noFetch = true
			case "pre-cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = "not a git repository"
				noFetch = true
			}
			var before squashWorkState
			if mode != "invalid repository" {
				before = f.state(t)
			}
			log := recordSquashGit(t, "")
			_, err := f.invoke(ctx, deps, base)
			require.ErrorContains(t, err, want)
			if mode == "pre-cancelled" {
				require.ErrorIs(t, err, context.Canceled)
			}
			commands := squashCommands(t, log)
			if mode == "incomplete source" {
				require.Equal(t, 1, strings.Count(commands, "fetch --no-tags"))
			}
			requireNoSquashMutation(t, commands)
			if noFetch {
				require.NotContains(t, commands, "fetch ")
			}
			if mode != "invalid repository" {
				require.Equal(t, before, f.state(t))
			}
		})
	}
}

func TestStandaloneSquashBoundedExhaustion(t *testing.T) {
	f := newSquashHistoryFixture(t)
	before := f.state(t)
	boundary := filepath.Join(t.TempDir(), "shallow")
	require.NoError(t, os.WriteFile(boundary, []byte("boundary-0\n"), 0600))
	// Only exceptional ancestry responses are controlled: every successful fetch
	// advances the observed boundary, forcing all 20 production iterations.
	shim := `case "$1" in
 merge-base) exit 1 ;;
 rev-parse) if [ "$2" = --git-path ]; then printf '%s\n' ` + shellcmd.QuotePOSIX(boundary) + `; exit 0; fi ;;
 rev-list) if [ "$2" = --max-parents=0 ]; then cat ` + shellcmd.QuotePOSIX(boundary) + `; exit 0; fi ;;
 fetch) printf '%s\n' "$3" > ` + shellcmd.QuotePOSIX(boundary) + `; exit 0 ;;
esac
`
	log := recordSquashGit(t, shim)
	_, err := f.invoke(context.Background(), f.deps(), f.base)
	require.ErrorContains(t, err, "verify squash ancestry")
	require.ErrorContains(t, err, "ancestry depth limit reached")
	commands := squashCommands(t, log)
	requireNoSquashMutation(t, commands)
	var depths []string
	for _, line := range strings.Split(commands, "\n") {
		if strings.HasPrefix(line, "fetch ") {
			depths = append(depths, strings.Fields(line)[2])
		}
	}
	require.Len(t, depths, 20)
	for i, depth := range depths {
		require.Equal(t, fmt.Sprintf("--depth=%d", 2<<i), depth)
	}
	require.Equal(t, before, f.state(t))
}

func TestStandaloneSquashCompleteHistoryOffline(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(fmt.Sprintf("base=head=%t", same), func(t *testing.T) {
			f := newSquashHistoryFixture(t)
			runGit(t, f.repo, "git", "fetch", "--unshallow", f.source)
			if same {
				f.base = f.candidate
			}
			before := f.state(t)
			deps := ops.NewOpDependenciesBuilder().WithWorktreePath(f.repo).WithGitContext(ops.GitExecutionContext{BaseRepo: "/unavailable"}).Build()
			log := recordSquashGit(t, "")
			out, err := f.invoke(context.Background(), deps, f.base)
			require.NoError(t, err)
			require.NotContains(t, squashCommands(t, log), "fetch ")
			require.Equal(t, before.tree, revParse(t, f.repo, "HEAD^{tree}"))
			if same {
				require.Equal(t, before.head, out["squashed_hash"])
				requireNoSquashMutation(t, squashCommands(t, log))
			} else {
				require.Equal(t, f.base, revParse(t, f.repo, "HEAD^"))
			}
		})
	}
}

func TestStandaloneSquashHTTPProfileAndCancellation(t *testing.T) {
	for _, mode := range []string{"profile", "authorization", "transport", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newSquashHistoryFixture(t)
			before := f.state(t)
			backendPath := filepath.Join(strings.TrimSpace(runGitOutput(t, f.repo, "git", "--exec-path")), "git-http-backend")
			if _, err := os.Stat(backendPath); os.IsNotExist(err) {
				t.Skip("Git smart HTTP backend not installed")
			}
			backend := &cgi.Handler{Path: backendPath, Root: "/", Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(f.source), "GIT_HTTP_EXPORT_ALL=1"}}
			reached := make(chan struct{}, 1)
			release := make(chan struct{})
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch mode {
				case "authorization":
					http.Error(w, "denied", 403)
					return
				case "transport":
					http.Error(w, "unavailable", 503)
					return
				case "cancel":
					select {
					case reached <- struct{}{}:
					default:
					}
					<-release
					return
				}
				backend.ServeHTTP(w, r)
			}))
			defer server.Close()
			profile := gitprofile.Profile{Version: 1, Entries: []gitprofile.Entry{{URL: server.URL, RewriteFrom: []string{"https://squash-history.invalid"}}}}
			data, err := json.Marshal(profile)
			require.NoError(t, err)
			key, err := gitprofile.Key("history")
			require.NoError(t, err)
			scope, err := gitprofile.NewScope(context.Background(), gitprofile.Selection{Value: "history", Explicit: true}, jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{key: data}}, nil, "")
			require.NoError(t, err)
			defer scope.Close()
			ctx, cancel := context.WithCancel(scope.Context())
			defer cancel()
			deps := ops.NewOpDependenciesBuilder().WithWorktreePath(f.repo).WithGitContext(ops.GitExecutionContext{BaseRepo: "https://squash-history.invalid/source"}).Build()
			log := recordSquashGit(t, "")
			result := make(chan error, 1)
			go func() { _, err := f.invoke(ctx, deps, f.base); result <- err }()
			if mode == "cancel" {
				select {
				case <-reached:
				case <-time.After(10 * time.Second):
					close(release)
					t.Fatal("fetch did not reach transport barrier")
				}
				cancel()
				close(release)
			}
			select {
			case err = <-result:
			case <-time.After(10 * time.Second):
				t.Fatal("squash did not finish promptly")
			}
			require.Positive(t, requests.Load(), "profile must rewrite the scoped source to the real server")
			if mode == "profile" {
				require.NoError(t, err)
				require.Equal(t, before.tree, revParse(t, f.repo, "HEAD^{tree}"))
				require.Equal(t, before.delta, f.state(t).delta)
			} else {
				require.ErrorContains(t, err, "verify squash ancestry")
				requireNoSquashMutation(t, squashCommands(t, log))
				require.Equal(t, before, f.state(t))
				if mode == "cancel" {
					require.ErrorIs(t, err, context.Canceled)
				}
				if mode == "authorization" {
					require.ErrorContains(t, err, "403")
				}
				if mode == "transport" {
					require.ErrorContains(t, err, "503")
				}
			}
		})
	}
}

func TestStandaloneSquashRegisteredEmptyAndIdentityFallback(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, empty := range []bool{true, false} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			repo := t.TempDir()
			runGit(t, repo, "git", "init", "-b", "main")
			commit := func(subject string) {
				runGit(t, repo, "git", "-c", "user.name=Original", "-c", "user.email=original@example.test", "commit", "--allow-empty", "-m", subject)
			}
			base := ""
			if !empty {
				commit("base")
				base = revParse(t, repo, "HEAD")
			}
			commit("first")
			commit("second")
			before := revParse(t, repo, "HEAD")
			g := ops.GitExecutionContext{BaseRepo: "/unavailable", ResolvedBaseHash: base}
			if empty {
				g.Workspace = &contextual.WorkspaceContext{Initial: "empty"}
			}
			deps := ops.NewOpDependenciesBuilder().WithWorktreePath(repo).WithGitContext(g).Build()
			log := recordSquashGit(t, "")
			out, err := GetSquashOp().TaskChain()[0].Invoke(deps, context.Background(), map[string]any{"repo_path": repo, "base_hash": base, "author": "Owner <owner@example.test>"})
			require.NoError(t, err)
			require.NotContains(t, squashCommands(t, log), "fetch ")
			require.Equal(t, map[string]any{"base_hash": base, "persist_hash": before}, out["squashed_commits"])
			require.Contains(t, runGitOutput(t, repo, "git", "show", "-s", "--format=%B", "HEAD"), "first")
			require.Contains(t, runGitOutput(t, repo, "git", "show", "-s", "--format=%B", "HEAD"), "second")
			require.Equal(t, "Owner <owner@example.test>|Owner <owner@example.test>", strings.TrimSpace(runGitOutput(t, repo, "git", "show", "-s", "--format=%an <%ae>|%cn <%ce>", "HEAD")))
			if empty {
				require.Equal(t, "1", strings.TrimSpace(runGitOutput(t, repo, "git", "rev-list", "--count", "HEAD")))
			} else {
				require.Equal(t, base, revParse(t, repo, "HEAD^"))
			}
			require.Equal(t, base, deps.GitContext().ResolvedBaseHash)
		})
	}
}
