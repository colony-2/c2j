package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/buildinfo"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/hookpolicy"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
)

func TestHookPermissionCLI(t *testing.T) {
	t.Setenv(gitprofile.SocketEnv, "")
	rt := toy.New()
	server := httptest.NewServer(remote.NewServer(rt))
	defer server.Close()
	hook := ".c2/hooks/check.yaml"
	repo := "https://example.com/service"
	run := func(args ...string) string {
		t.Helper()
		c := newRootCmd(buildinfo.Info{})
		c.SetArgs(append([]string{"hooks", "perms", "--json", "--jobdb", server.URL + "/tenant"}, args...))
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&out)
		require.NoError(t, c.Execute(), out.String())
		require.True(t, json.Valid(out.Bytes()), "--json must emit only JSON for every operation: %s", out.String())
		return out.String()
	}
	run("add", hook, "--tenant-wide", "--secret", "A", "--submit-jobs", "--trust-candidate-code")
	run("remove", hook, "--cell", repo, "--secret", "A")
	snapshot, err := rt.GetTenantConfiguration(context.Background(), "tenant")
	require.NoError(t, err)
	s, err := hookpolicy.Capture(snapshot, repo)
	require.NoError(t, err)
	e, source := s.Select(hook)
	require.Equal(t, "cell", source)
	require.Empty(t, e.Perms.JobDB.Secrets.Resolve)
	require.True(t, e.Perms.JobDB.Jobs.Submit)
	run("remove", hook, "--cell", repo, "--submit-jobs")
	out := run("get", hook, "--cell", repo)
	require.Contains(t, out, `"source":"cell"`)
	run("inherit", hook, "--cell", repo)
	out = run("get", hook, "--cell", repo)
	require.Contains(t, out, `"source":"tenant"`)
	entry := `{"perms":{"jobdb":{"jobs":{"submit":true}},"limit":{"jobdb":{"jobs":{"submit":true}}}},"source_trust":{"jobdb.jobs.submit":{"mode":"candidate"}}}`
	run("set", hook, "--cell", repo, "--policy-json", entry)
	out = run("list", "--all-cells")
	require.Contains(t, out, hookpolicy.CellKey(repo))
	var v any
	require.NoError(t, json.Unmarshal([]byte(out), &v))
	run("clear", hook, "--tenant-wide")
	out = run("get", hook, "--cell", repo)
	require.Contains(t, out, `"submit":true`)
}
func TestHookPermissionCLIRejectsAmbiguousScopeAndExecution(t *testing.T) {
	for _, args := range [][]string{{"get", "hook.yaml"}, {"get", "hook.yaml", "--tenant-wide", "--cell", "foo"}, {"add", "hook.yaml", "--tenant-wide", "--secret", "A"}, {"inherit", "hook.yaml", "--tenant-wide"}} {
		c := newHooksCmd()
		c.SetArgs(append([]string{"perms"}, args...))
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		require.Error(t, c.Execute())
	}
	t.Setenv(gitprofile.SocketEnv, "live-execution")
	c := newHooksCmd()
	c.SetArgs([]string{"perms", "get", "hook.yaml", "--tenant-wide"})
	require.ErrorContains(t, c.Execute(), "provisioning context")
}

type hookConflictRuntime struct{ *toy.Runtime }

func (r hookConflictRuntime) SetTenantConfigurationValue(ctx context.Context, req jobdb.SetTenantConfigurationValueRequest) (string, error) {
	if req.Name == hookpolicy.TenantKey {
		_, err := r.Runtime.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: req.TenantID, Name: "concurrent_edit", Value: json.RawMessage(`true`)})
		if err != nil {
			return "", err
		}
	}
	return r.Runtime.SetTenantConfigurationValue(ctx, req)
}
func TestHookPermissionCLIRejectsConcurrentEdit(t *testing.T) {
	t.Setenv(gitprofile.SocketEnv, "")
	rt := toy.New()
	server := httptest.NewServer(remote.NewServer(hookConflictRuntime{rt}))
	defer server.Close()
	c := newRootCmd(buildinfo.Info{})
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetArgs([]string{"hooks", "perms", "add", "hook.yaml", "--tenant-wide", "--secret", "A", "--trust-candidate-code", "--jobdb", server.URL + "/tenant"})
	require.ErrorContains(t, c.Execute(), "reread before retrying")
	snapshot, err := rt.GetTenantConfiguration(context.Background(), "tenant")
	require.NoError(t, err)
	require.NotContains(t, snapshot.Values, hookpolicy.TenantKey)
	require.Contains(t, snapshot.Values, "concurrent_edit")
}

func TestHookPermissionCLITextOutput(t *testing.T) {
	t.Setenv(gitprofile.SocketEnv, "")
	server := httptest.NewServer(remote.NewServer(toy.New()))
	defer server.Close()
	run := func(args ...string) string {
		t.Helper()
		cmd := newRootCmd(buildinfo.Info{})
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		cmd.SetArgs(append([]string{"hooks", "perms", "--jobdb", server.URL + "/tenant"}, args...))
		require.NoError(t, cmd.Execute(), stderr.String())
		require.Empty(t, stderr.String())
		require.False(t, json.Valid(stdout.Bytes()), "default output should be readable text")
		return stdout.String()
	}
	hook := ".c2/hooks/check.yaml"
	repo := "https://example.com/service"
	sha := strings.Repeat("a", 40)
	require.Contains(t, run("list", "--all-cells"), "No hook policies configured.")
	require.Contains(t, run("list", "--tenant-wide"), "No hook entries configured.")
	require.Contains(t, run("get", hook, "--tenant-wide"), "Source: none (no grants)")
	out := run("add", hook, "--tenant-wide", "--secret", "Z_TOKEN", "--secret", "A_TOKEN", "--submit-jobs", "--candidate-commit", sha)
	for _, want := range []string{"Updated hook permissions.", "Tenant: tenant", "Scope: tenant defaults", "Revision:", "Hook: " + hook, "Source: tenant default", "A_TOKEN (trust: candidate commit " + sha + ")", "Submit jobs: allowed"} {
		require.Contains(t, out, want)
	}
	require.Less(t, strings.Index(out, "A_TOKEN"), strings.Index(out, "Z_TOKEN"))
	require.Contains(t, run("get", hook, "--cell", repo), "Source: tenant default")
	out = run("remove", hook, "--cell", repo, "--secret", "A_TOKEN")
	require.Contains(t, out, "Source: cell override")
	require.NotContains(t, out, "A_TOKEN")
	out = run("clear", hook, "--cell", repo)
	require.Contains(t, out, "explicit deny")
	require.Contains(t, out, "Source: cell override")
	require.Contains(t, out, "Secret resolution: none")
	require.Contains(t, out, "Submit jobs: denied")
	out = run("inherit", hook, "--cell", repo)
	require.Contains(t, out, "tenant inheritance restored")
	require.Contains(t, out, "Source: tenant default")
	require.Contains(t, out, "A_TOKEN")
	entry := `{"perms":{"jobdb":{"jobs":{"submit":true}},"limit":{"jobdb":{"jobs":{"submit":true}}}},"source_trust":{"jobdb.jobs.submit":{"mode":"candidate"}}}`
	out = run("set", hook, "--cell", repo, "--policy-json", entry)
	require.Contains(t, out, "Updated hook permissions.")
	require.Contains(t, out, "Submit jobs: allowed (trust: candidate code)")
	require.Contains(t, out, "Limits:\n    Secret resolution: none\n    Submit jobs: allowed")
	out = run("list", "--all-cells")
	require.Contains(t, out, "Tenant defaults")
	require.Contains(t, out, "Cell: "+repo)
	require.NotContains(t, out, hookpolicy.CellKey(repo))
	require.Less(t, strings.Index(out, "Tenant defaults"), strings.Index(out, "Cell: "+repo))
	require.Contains(t, out, "Updates affect new jobs")
}
