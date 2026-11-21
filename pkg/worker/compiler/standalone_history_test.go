package compiler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/squashrebasemerge"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real compiler tasks discard their worktrees after persistence. Each following
// invocation must consume the accepted bundle and original recorded base.
func TestStandaloneSquashPersistedRefresh(t *testing.T) {
	for _, failedFetch := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed_fetch=%t", failedFetch), func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			base := workspaceRepo(t, source, "source")
			realGit, err := exec.LookPath("git")
			require.NoError(t, err)
			cmd := exec.Command(realGit, "--exec-path")
			data, err := cmd.Output()
			require.NoError(t, err)
			backendPath := filepath.Join(strings.TrimSpace(string(data)), "git-http-backend")
			if _, err := os.Stat(backendPath); os.IsNotExist(err) {
				t.Skip("Git smart HTTP backend not installed")
			}
			backend := &cgi.Handler{Path: backendPath, Root: "/", Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if failedFetch && r.Method == "POST" {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					if bytes.Contains(body, []byte("deepen 2")) {
						http.Error(w, "ancestry fetch unavailable", 503)
						return
					}
				}
				backend.ServeHTTP(w, r)
			}))
			defer server.Close()
			url := server.URL + "/source"
			template := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(template, "config"), []byte(fmt.Sprintf("[url %q]\n insteadOf = %s\n", url, source)), 0600))
			t.Setenv("GIT_TEMPLATE_DIR", template)
			var candidate, tree, correction, newBase string
			var dirs []string
			type input struct {
				Stage string `json:"stage"`
			}
			probe := coreops.NewActivityMappedOpV2[input, map[string]any](coreops.OpMetadata{Type: "history_probe"}, func(deps coreops.OpDependencies, ctx context.Context, in input) (map[string]any, error) {
				repo := deps.WorktreePath()
				g := deps.GitContext()
				dirs = append(dirs, repo)
				git := func(where string, args ...string) string {
					out, e := common.ExecuteGitCommand(ctx, where, args...)
					assert.NoError(t, e)
					return strings.TrimSpace(string(out))
				}
				original := git(repo, "rev-parse", "HEAD")
				if in.Stage == "prepare" {
					for i := 0; i < 8; i++ {
						git(source, "commit", "--allow-empty", "-m", fmt.Sprintf("published %d", i))
					}
					tip := git(source, "rev-parse", "HEAD")
					git(repo, "fetch", "--depth=1", url, tip)
					git(repo, "reset", "--hard", tip)
					assert.NoError(t, os.WriteFile(filepath.Join(repo, "binary"), []byte{0, 255, 128, 0, 10}, 0600))
					assert.NoError(t, os.WriteFile(filepath.Join(repo, "executable"), []byte("#!/bin/sh\nexit 0\n"), 0755))
					assert.NoError(t, os.Symlink("binary", filepath.Join(repo, "symbolic link")))
					git(repo, "rm", "identity")
					git(repo, "add", ".")
					git(repo, "commit", "-m", "unpublished correction")
					candidate = git(repo, "rev-parse", "HEAD")
					tree = git(repo, "rev-parse", "HEAD^{tree}")
					correction = git(repo, "diff", "--binary", "--full-index", base, "HEAD")
					_, e := common.ExecuteGitCommand(ctx, repo, "merge-base", "--is-ancestor", base, "HEAD")
					assert.Error(t, e)
					assert.False(t, common.CommitExists(ctx, source, candidate))
				} else {
					expectedBase := base
					if in.Stage == "rebased" || in.Stage == "repeated" {
						expectedBase = newBase
					}
					assert.Equal(t, expectedBase, g.ResolvedBaseHash)
					assert.Equal(t, expectedBase, g.ParentHash)
					assert.Equal(t, original, g.PersistHash, "restore must consume the latest accepted candidate")
					assert.Equal(t, correction, git(repo, "diff", "--binary", "--full-index", expectedBase, "HEAD"))
					if in.Stage == "failed" {
						assert.Equal(t, candidate, original)
						assert.Equal(t, tree, git(repo, "rev-parse", "HEAD^{tree}"))
					} else {
						assert.Equal(t, expectedBase, git(repo, "rev-parse", "HEAD^"))
					}
					if in.Stage == "squashed" {
						assert.Equal(t, tree, git(repo, "rev-parse", "HEAD^{tree}"))
						assert.NoError(t, os.WriteFile(filepath.Join(source, "upstream"), []byte("incorporated upstream\n"), 0644))
						git(source, "add", ".")
						git(source, "commit", "-m", "upstream after snapshot")
						newBase = git(source, "rev-parse", "HEAD")
					}
					if in.Stage == "rebased" || in.Stage == "repeated" {
						assert.FileExists(t, filepath.Join(repo, "upstream"))
					}
				}
				if t.Failed() {
					return nil, fmt.Errorf("saved workspace assertions failed; see test diagnostics")
				}
				return map[string]any{"head": git(repo, "rev-parse", "HEAD"), "target": newBase, "base": g.ResolvedBaseHash, "dir": repo}, nil
			})
			ws := workspaceTestWorker(t, squashrebasemerge.GetSquashOp(), thinpackrebase.GetRebaseOp(), probe)
			job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "source"}, GitBase: contextual.GitBaseContext{BaseRepo: source, BaseRef: "main"}}
			text := `id: workspace-test
sequence:
- id: prepare
  op: history_probe
  inputs: {stage: prepare}
- id: squash
  op: squash
  inputs: {commit_message: Preserved correction}
- id: squashed
  op: history_probe
  inputs: {stage: squashed}
- id: rebase
  op: rebase
  inputs: {target_base_hash: '${{ sequence.squashed.outputs.target }}'}
- id: rebased
  op: history_probe
  inputs: {stage: rebased}
- id: repeat
  op: squash
  inputs: {commit_message: Preserved correction again}
- id: repeated
  op: history_probe
  inputs: {stage: repeated}
outputs:
 first: '${{ sequence.squash.outputs }}'
 second: '${{ sequence.repeat.outputs }}'
 rebase: '${{ sequence.rebase.outputs }}'
 final: '${{ sequence.repeated.outputs }}'
`
			if failedFetch {
				text = `id: workspace-test
sequence:
- id: prepare
  op: history_probe
  inputs: {stage: prepare}
- id: squash
  op: squash
  catch:
  - when: true
    continue: {outputs: {rejected: true, message: '${{ failure.message }}'}}
- id: failed
  op: history_probe
  inputs: {stage: failed}
outputs:
 rejected: '${{ sequence.squash.outputs.rejected }}'
 message: '${{ sequence.squash.outputs.message }}'
 final: '${{ sequence.failed.outputs }}'
`
			}
			out, arts := runWorkspaceRecipe(t, ws, text, job)
			if failedFetch {
				require.Equal(t, true, out["rejected"])
				require.Contains(t, out["message"], "verify squash ancestry")
				require.Contains(t, out["message"], "503")
			} else {
				require.Equal(t, candidate, out["first"].(map[string]any)["squashed_commits"].(map[string]any)["persist_hash"])
				require.Equal(t, newBase, out["final"].(map[string]any)["base"])
				require.NotEqual(t, out["first"].(map[string]any)["squashed_hash"], out["second"].(map[string]any)["squashed_hash"])
			}
			require.NotEmpty(t, arts, "accepted snapshot artifacts must persist")
			seen := map[string]bool{}
			for _, dir := range dirs {
				require.False(t, seen[dir], "each compiler invocation must materialize a fresh worktree")
				seen[dir] = true
				require.NoDirExists(t, dir)
			}
		})
	}
}

// The supplied preservation oracle belongs to JobDB. These optional external
// paths let an owner run its exact saved objects through the normal compiler,
// without putting another repository's patch or objects into c2j's fixtures.
func TestStandaloneSquashSavedJobDBSnapshot(t *testing.T) {
	published, bundle, oracle := os.Getenv("C2J_TEST_SAVED_JOBDB_REPO"), os.Getenv("C2J_TEST_SAVED_JOBDB_BUNDLE"), os.Getenv("C2J_TEST_SAVED_JOBDB_ORACLE")
	if published == "" || bundle == "" || oracle == "" {
		t.Skip("exact saved JobDB objects/oracle not supplied; synthetic refresh coverage runs separately")
	}
	const base = "1dd75e9fd864d05c5b102781af4d8671519a5655"
	const publication = "05238600e743697cb902630ad9dfc6e1728ba744"
	const saved = "80a26ef4bfef90ea0439850ad00825838b290d13"
	root := t.TempDir()
	source := filepath.Join(root, "published-source")
	require.NoError(t, runGit(root, "git", "clone", "--shared", published, source))
	require.NoError(t, runGit(source, "git", "checkout", "-B", "main", publication))
	require.False(t, common.CommitExists(context.Background(), source, saved), "source must contain only published work")
	expected, err := os.ReadFile(oracle)
	require.NoError(t, err)
	sum := sha256.Sum256(expected)
	require.Equal(t, "97abfdc5bdcae66f3420da906c1ad230b8e0109a5dec9f39edf1be948606f52f", hex.EncodeToString(sum[:]))
	template := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(template, "config"), []byte("[url \"file:///\"]\n insteadOf = /\n"), 0600))
	t.Setenv("GIT_TEMPLATE_DIR", template)
	var tree, delta, index string
	var dirs []string
	type in struct {
		Setup bool `json:"setup"`
	}
	probe := coreops.NewActivityMappedOpV2[in, map[string]any](coreops.OpMetadata{Type: "saved_snapshot_probe"}, func(deps coreops.OpDependencies, ctx context.Context, input in) (map[string]any, error) {
		repo := deps.WorktreePath()
		dirs = append(dirs, repo)
		g := deps.GitContext()
		git := func(args ...string) string {
			out, e := common.ExecuteGitCommand(ctx, repo, args...)
			assert.NoError(t, e)
			return string(out)
		}
		if input.Setup {
			git("bundle", "unbundle", bundle)
			git("checkout", "--detach", saved)
			tree = git("rev-parse", "HEAD^{tree}")
			delta = git("diff", "--binary", "--full-index", publication, "HEAD")
			index = git("ls-files", "--stage", "-z")
			assert.Equal(t, string(expected), git("diff", "--binary", "--abbrev=7", publication, "HEAD"))
			assert.True(t, common.CommitExists(ctx, repo, base))
			_, e := common.ExecuteGitCommand(ctx, repo, "merge-base", "--is-ancestor", base, "HEAD")
			assert.Error(t, e, "actual saved fixture must hide ancestry")
		} else {
			active := base
			assert.Equal(t, active, g.ResolvedBaseHash)
			assert.Equal(t, active, g.ParentHash)
			assert.Equal(t, tree, git("rev-parse", "HEAD^{tree}"))
			assert.Equal(t, delta, git("diff", "--binary", "--full-index", publication, "HEAD"))
			assert.Equal(t, index, git("ls-files", "--stage", "-z"))
			assert.Equal(t, active+"\n", git("rev-parse", "HEAD^"))
			assert.Equal(t, string(expected), git("diff", "--binary", "--abbrev=7", publication, "HEAD"))
		}
		if t.Failed() {
			return nil, fmt.Errorf("saved workspace assertions failed; see test diagnostics")
		}
		return map[string]any{"head": strings.TrimSpace(git("rev-parse", "HEAD")), "base": g.ResolvedBaseHash}, nil
	})
	ws := workspaceTestWorker(t, squashrebasemerge.GetSquashOp(), thinpackrebase.GetRebaseOp(), probe)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "source"}, GitBase: contextual.GitBaseContext{BaseRepo: source, BaseRef: base}}
	out, _ := runWorkspaceRecipe(t, ws, `id: workspace-test
sequence:
- id: setup
  op: saved_snapshot_probe
  inputs: {setup: true}
- id: first
  op: squash
  inputs: {commit_message: Exact saved correction}
- id: restored
  op: saved_snapshot_probe
- id: second
  op: squash
  inputs: {commit_message: Repeated exact saved correction}
- id: repeated
  op: saved_snapshot_probe
outputs:
 first: '${{ sequence.first.outputs }}'
 final: '${{ sequence.repeated.outputs }}'
`, job)
	require.Equal(t, saved, out["first"].(map[string]any)["squashed_commits"].(map[string]any)["persist_hash"])
	require.Equal(t, base, out["final"].(map[string]any)["base"])
	seen := map[string]bool{}
	for _, dir := range dirs {
		require.False(t, seen[dir])
		seen[dir] = true
		require.NoDirExists(t, dir)
	}
}
