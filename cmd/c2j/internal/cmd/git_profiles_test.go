package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/buildinfo"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
)

func TestGitCredentialHelperProcess(t *testing.T) {
	if os.Getenv("C2J_GIT_HELPER_TEST") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	c := newRootCmd(buildinfo.Info{})
	c.SetArgs(args)
	if err := c.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
func TestGitInvokesScopedCredentialHelper(t *testing.T) {
	t.Setenv("C2J_GIT_HELPER_TEST", "1")
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "c2j helper")
	text := "#!/bin/sh\nexec '" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "' -test.run=TestGitCredentialHelperProcess -- \"$@\"\n"
	require.NoError(t, os.WriteFile(wrapper, []byte(text), 0700))
	p := gitprofile.Profile{Version: 1, Entries: []gitprofile.Entry{{URL: "https://git.example/team/", RewriteFrom: []string{"https://source.example/team/"}, Username: "bot", SecretRef: "token"}}}
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	s, err := gitprofile.NewScope(context.Background(), gitprofile.Selection{}, jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{gitprofile.ReadOnlyKey: json.RawMessage(`"work"`), gitprofile.ProfilePrefix + "work": raw}}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "private-value"}}}, nil
	}, wrapper)
	require.NoError(t, err)
	defer s.Close()
	ctx := gitprofile.WithScope(context.Background(), s)
	for _, tc := range []struct {
		host, path string
		allow      bool
	}{{"git.example", "team/repo.git", true}, {"git.example", "other/repo.git", false}, {"git.example.evil", "team/repo.git", false}} {
		c := gitprofile.Command(ctx, "credential", "fill")
		c.Dir = dir
		c.Stdin = strings.NewReader("protocol=https\nhost=" + tc.host + "\npath=" + tc.path + "\n\n")
		out, err := c.CombinedOutput()
		if tc.allow {
			require.NoError(t, err, string(out))
			require.Contains(t, string(out), "password=private-value")
		} else {
			require.Error(t, err)
			require.NotContains(t, string(out), "private-value")
		}
	}
	// Verify rewrite configuration without contacting any remote.
	require.NoError(t, exec.Command("git", "init", dir).Run())
	require.NoError(t, exec.Command("git", "-C", dir, "remote", "add", "origin", "https://source.example/team/repo.git").Run())
	c := gitprofile.Command(ctx, "-C", dir, "remote", "get-url", "origin")
	out, err := c.Output()
	require.NoError(t, err)
	require.Equal(t, "https://git.example/team/repo.git\n", string(out))
	var socket string
	for _, item := range gitprofile.Env(ctx, nil) {
		if v, ok := strings.CutPrefix(item, gitprofile.SocketEnv+"="); ok {
			socket = v
		}
	}
	t.Setenv(gitprofile.SocketEnv, socket)
	root := newRootCmd(buildinfo.Info{})
	root.SetArgs([]string{"git", "clear"})
	require.NoError(t, root.Execute())
	c = gitprofile.Command(ctx, "-C", dir, "remote", "get-url", "origin")
	out, err = c.Output()
	require.NoError(t, err)
	require.Equal(t, "https://source.example/team/repo.git\n", string(out))
	c = gitprofile.Command(ctx, "credential", "fill")
	c.Stdin = strings.NewReader("protocol=https\nhost=git.example\npath=team/repo.git\n\n")
	out, err = c.CombinedOutput()
	require.Error(t, err)
	require.NotContains(t, string(out), "private-value")
}
func TestGitProfileManagement(t *testing.T) {
	rt := toy.New()
	server := httptest.NewServer(remote.NewServer(rt))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "profile.yaml")
	require.NoError(t, os.WriteFile(file, []byte("version: 1\nentries:\n  - url: https://git.example/team/\n    username: bot\n    secret_ref: externally-managed\n"), 0600))
	run := func(args ...string) (string, error) {
		root := newRootCmd(buildinfo.Info{})
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(append([]string{"git", "--tenant", "test", "--url", server.URL}, args...))
		err := root.Execute()
		return out.String(), err
	}
	_, err := run("set", "work", "--file", file)
	require.NoError(t, err)
	out, err := run("get", "work")
	require.NoError(t, err)
	require.Contains(t, out, "externally-managed")
	require.NotContains(t, out, "secret_file")
	out, err = run("list")
	require.NoError(t, err)
	require.JSONEq(t, `["work"]`, out)
	snapshot, err := rt.GetTenantConfiguration(context.Background(), "test")
	require.NoError(t, err)
	require.Contains(t, snapshot.Values, "_c2j_git_profile_work")
	_, err = run("remove", "work")
	require.NoError(t, err)
	out, err = run("list")
	require.NoError(t, err)
	require.JSONEq(t, `[]`, out)
}
func TestExecutionCommandsRequireLiveScope(t *testing.T) {
	t.Setenv(gitprofile.SocketEnv, "")
	for _, args := range [][]string{{"secret", "get", "token"}, {"config", "get", "_c2j_git_profile_work"}, {"git", "activate", "work"}, {"git", "clear"}} {
		root := newRootCmd(buildinfo.Info{})
		root.SetArgs(args)
		require.ErrorContains(t, root.Execute(), "live c2j execution")
	}
	root := newRootCmd(buildinfo.Info{})
	root.SetArgs([]string{"secret", "git-credential", "get"})
	root.SetIn(strings.NewReader("protocol=https\nhost=x\nhost=y\n\n"))
	var out bytes.Buffer
	root.SetOut(&out)
	require.Error(t, root.Execute())
	require.Equal(t, "quit=true\n\n", out.String())
}

func TestSuppliedCredentialStoredSeparately(t *testing.T) {
	ctx := context.Background()
	rt, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
	require.NoError(t, err)
	defer rt.Close(ctx)
	server := httptest.NewServer(remote.NewServer(rt))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "profile.yaml")
	require.NoError(t, os.WriteFile(file, []byte("version: 1\nentries:\n  - url: https://git.example/\n    username: bot\n    secret_file: '-'\n"), 0600))
	root := newRootCmd(buildinfo.Info{})
	root.SetArgs([]string{"git", "set", "work", "--file", file, "--tenant", "test", "--url", server.URL})
	root.SetIn(strings.NewReader("supplied-private-value"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	require.NoError(t, root.Execute())
	require.NotContains(t, out.String(), "supplied-private-value")
	snapshot, err := rt.GetTenantConfiguration(ctx, "test")
	require.NoError(t, err)
	p, err := gitprofile.Load(snapshot, "work")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(p.Entries[0].SecretRef, gitprofile.SecretPrefix))
	require.NotContains(t, string(snapshot.Values[gitprofile.ProfilePrefix+"work"]), "supplied-private-value")
	handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobType: "probe", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	lease, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: handle.JobKey, WorkerID: "test", Routes: []jobdb.Route{{JobType: "probe"}}, LeaseDuration: time.Minute})
	require.NoError(t, err)
	defer lease.StopKeepAlive()
	values, err := lease.ResolveSecrets(ctx, []string{p.Entries[0].SecretRef})
	require.NoError(t, err)
	require.Equal(t, "supplied-private-value", values.Secrets[p.Entries[0].SecretRef].Value)
}

func TestRoutedGitCloneFetchAndServerEnforcedReadOnly(t *testing.T) {
	t.Setenv("C2J_GIT_HELPER_TEST", "1")
	t.Setenv("GIT_SSL_NO_VERIFY", "true") // Local test server certificate only.
	git, err := exec.LookPath("git")
	require.NoError(t, err)
	root := t.TempDir()
	bare := filepath.Join(root, "repo.git")
	seed := filepath.Join(t.TempDir(), "seed")
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command(git, args...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "--bare", bare)
	run("--git-dir="+bare, "config", "http.receivepack", "true")
	run("init", "-b", "main", seed)
	run("-C", seed, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	run("-C", seed, "push", bare, "HEAD:refs/heads/main")
	run("--git-dir="+bare, "symbolic-ref", "HEAD", "refs/heads/main")
	backend := &cgi.Handler{Path: git, Args: []string{"http-backend"}, Dir: root, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "bot" || (password != "ro-value" && password != "rw-value") {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if password == "ro-value" && (strings.HasSuffix(r.URL.Path, "git-receive-pack") || r.URL.Query().Get("service") == "git-receive-pack") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer server.Close()
	wrapper := filepath.Join(t.TempDir(), "c2j")
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+strings.ReplaceAll(os.Args[0], "'", "'\\''")+"' -test.run=TestGitCredentialHelperProcess -- \"$@\"\n"), 0700))
	snapshot := jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{}}
	for _, name := range []string{"ro", "rw"} {
		raw, _ := json.Marshal(gitprofile.Profile{Version: 1, Entries: []gitprofile.Entry{{URL: server.URL + "/", RewriteFrom: []string{"https://source.invalid/"}, Username: "bot", SecretRef: name}}})
		snapshot.Values[gitprofile.ProfilePrefix+name] = raw
	}
	resolve := func(_ context.Context, names []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{names[0]: {Value: names[0] + "-value"}}}, nil
	}
	ro, err := gitprofile.NewScope(context.Background(), gitprofile.Selection{Explicit: true, Value: "ro"}, snapshot, resolve, wrapper)
	require.NoError(t, err)
	defer ro.Close()
	rw, err := gitprofile.NewScope(context.Background(), gitprofile.Selection{Explicit: true, Value: "rw"}, snapshot, resolve, wrapper)
	require.NoError(t, err)
	defer rw.Close()
	roCtx, rwCtx := gitprofile.WithScope(context.Background(), ro), gitprofile.WithScope(context.Background(), rw)
	clone := filepath.Join(t.TempDir(), "clone")
	for _, args := range [][]string{{"ls-remote", "https://source.invalid/repo.git"}, {"clone", "https://source.invalid/repo.git", clone}, {"-C", clone, "fetch", "origin"}} {
		out, err := gitprofile.Command(roCtx, args...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("-C", clone, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "publish")
	out, err := gitprofile.Command(roCtx, "-C", clone, "push", "origin", "HEAD:refs/heads/main").CombinedOutput()
	require.Error(t, err)
	require.NotContains(t, string(out), "ro-value")
	out, err = gitprofile.Command(rwCtx, "-C", clone, "push", "origin", "HEAD:refs/heads/main").CombinedOutput()
	require.NoError(t, err, string(out))
	local, err := exec.Command(git, "-C", clone, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	published, err := exec.Command(git, "--git-dir="+bare, "rev-parse", "main").Output()
	require.NoError(t, err)
	require.Equal(t, string(local), string(published))
}
