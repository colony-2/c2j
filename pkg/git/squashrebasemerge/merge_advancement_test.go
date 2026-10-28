package squashrebasemerge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type advancementFixture struct {
	repo, remote, root, base, candidate, upstream string
}

func newAdvancementFixture(t *testing.T) advancementFixture {
	t.Helper()
	dir := t.TempDir()
	f := advancementFixture{repo: filepath.Join(dir, "work"), remote: filepath.Join(dir, "remote.git")}
	initBareRemote(t, f.remote)
	require.NoError(t, os.Mkdir(f.repo, 0700))
	runGit(t, f.repo, "git", "init", "-b", "main")
	require.NoError(t, common.EnsureGitIdentity(context.Background(), f.repo))
	commit := func(name string) string {
		writeFile(t, f.repo, name, name)
		runGit(t, f.repo, "git", "add", ".")
		runGit(t, f.repo, "git", "commit", "-m", name)
		return revParse(t, f.repo, "HEAD")
	}
	f.root = commit("root")
	f.base = commit("base")
	runGit(t, f.repo, "git", "push", f.remote, "HEAD:main")
	f.upstream = commit("upstream")
	runGit(t, f.repo, "git", "push", f.remote, "HEAD:other")
	runGit(t, f.repo, "git", "reset", "--hard", f.base)
	f.candidate = commit("candidate")
	return f
}

func (f advancementFixture) input() MergeInput {
	return MergeInput{RepoPath: f.repo, UpstreamRepo: f.remote, UpstreamBranch: "main", PreparedBaseHash: f.base}
}

func requireAdvancement(t *testing.T, err error, f advancementFixture, phase string) {
	t.Helper()
	var app *jobdb.AppError
	require.ErrorAs(t, err, &app)
	require.Equal(t, map[string]any{
		"code": MergeUpstreamAdvancedCode, "candidate_hash": f.candidate,
		"prepared_base_hash": f.base, "upstream_hash": f.upstream,
		"target_branch": "refs/heads/main", "phase": phase,
	}, app.Payload.Attrs)
	require.Equal(t, f.candidate, revParse(t, f.repo, "HEAD"))
	require.Equal(t, f.upstream, revParse(t, f.remote, "main"))
}

func TestMergeClassifiesPreparedBaseAdvancement(t *testing.T) {
	for _, mode := range []string{"explicit", "managed", "abbreviated", "shallow history", "push race"} {
		t.Run(mode, func(t *testing.T) {
			f := newAdvancementFixture(t)
			input := f.input()
			phase := "preflight"
			if mode == "shallow history" {
				require.NoError(t, os.WriteFile(filepath.Join(f.repo, ".git/shallow"), []byte(f.base+"\n"), 0600))
			}
			if mode == "push race" {
				phase = "push"
				hook := fmt.Sprintf("#!/bin/sh\ngit --git-dir='%s' update-ref refs/heads/main %s\n", f.remote, f.upstream)
				require.NoError(t, os.WriteFile(filepath.Join(f.repo, ".git/hooks/pre-push"), []byte(hook), 0700))
			} else {
				runGit(t, f.remote, "git", "update-ref", "refs/heads/main", f.upstream)
			}
			var err error
			if mode == "managed" {
				input.PreparedBaseHash = ""
				deps := ops.NewOpDependenciesBuilder().WithWorktreePath(f.repo).WithGitContext(ops.GitExecutionContext{BaseRepo: f.remote, ResolvedBaseHash: f.base}).Build()
				_, err = executeMerge(deps, context.Background(), input)
				require.Equal(t, f.base, deps.GitContext().ResolvedBaseHash)
			} else {
				if mode == "abbreviated" {
					input.PreparedBaseHash = f.base[:12]
				}
				_, err = merge(context.Background(), input, false)
			}
			requireAdvancement(t, err, f, phase)
		})
	}
}

func TestMergeDoesNotMisclassifyOtherFailures(t *testing.T) {
	for _, mode := range []string{"no base", "wrong base", "unknown base", "rewritten", "initial collision", "dirty", "invalid ref", "shallow", "hook rejection", "lock failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newAdvancementFixture(t)
			input := f.input()
			runGit(t, f.remote, "git", "update-ref", "refs/heads/main", f.upstream)
			switch mode {
			case "no base":
				input.PreparedBaseHash = ""
			case "wrong base":
				input.PreparedBaseHash = f.candidate
			case "unknown base":
				input.PreparedBaseHash = strings.Repeat("a", 40)
			case "rewritten":
				tree := revParse(t, f.remote, "main^{tree}")
				rewritten := strings.TrimSpace(runGitOutput(t, f.remote, "git", "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit-tree", tree, "-p", f.root, "-m", "rewritten"))
				runGit(t, f.remote, "git", "update-ref", "refs/heads/main", rewritten)
			case "dirty":
				writeFile(t, f.repo, "dirty", "dirty")
			case "invalid ref":
				input.UpstreamBranch = "bad..ref"
			case "shallow":
				// The unpublished candidate cannot be deepened from the server.
				require.NoError(t, os.WriteFile(filepath.Join(f.repo, ".git/shallow"), []byte(f.candidate+"\n"), 0600))
			case "hook rejection", "lock failure":
				runGit(t, f.remote, "git", "update-ref", "refs/heads/main", f.base)
				hook := fmt.Sprintf("#!/bin/sh\ngit --git-dir='%s' update-ref refs/heads/main %s\n", f.remote, f.upstream)
				if mode == "hook rejection" {
					hook += "exit 1\n"
				} else {
					hook += fmt.Sprintf("touch '%s/refs/heads/main.lock'\n", f.remote)
				}
				require.NoError(t, os.WriteFile(filepath.Join(f.repo, ".git/hooks/pre-push"), []byte(hook), 0700))
			}
			_, err := merge(context.Background(), input, mode == "initial collision")
			require.Error(t, err)
			require.False(t, isMergeAdvancement(err), "%v", err)
			require.Equal(t, f.candidate, revParse(t, f.repo, "HEAD"))
		})
	}
}

func TestPushRejectionClassificationRequiresRefStatus(t *testing.T) {
	f := advancementFixture{candidate: strings.Repeat("a", 40), upstream: strings.Repeat("b", 40), base: strings.Repeat("c", 40)}
	for _, tt := range []struct {
		status, stderr string
		want           bool
	}{
		{"[rejected] (fetch first)", "", true},
		{"[rejected] (non-fast-forward)", "", true},
		{"[remote rejected] (non-fast-forward)", "", true},
		{"[remote rejected] (pre-receive hook declined)", "", false},
		{"[remote failure] (remote failed to report status)", "", false},
		{"[remote rejected] (failed to update ref)", "permission denied", false},
		{"[remote rejected] (failed to update ref)", fmt.Sprintf("cannot lock ref 'refs/heads/main': is at %s but expected %s", f.upstream, f.base), true},
		{"[remote rejected] (failed to update ref)", fmt.Sprintf("cannot lock ref 'refs/heads/other': is at %s but expected %s", f.upstream, f.base), false},
	} {
		stdout := "!\t" + f.candidate + ":refs/heads/main\t" + tt.status + "\n"
		require.Equal(t, tt.want, pushRejectedForAdvancement(stdout, tt.stderr, f.candidate, "refs/heads/main"), tt.status)
		require.False(t, pushRejectedForAdvancement("", stdout+tt.stderr, f.candidate, "refs/heads/main"), "stderr cannot supply ref status")
		require.False(t, pushRejectedForAdvancement(stdout, tt.stderr, f.candidate, "refs/heads/other"), "status must name the target")
	}
}
