package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
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
		c.SetArgs(append([]string{"hooks", "perms", "--jobdb", server.URL + "/tenant"}, args...))
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&out)
		require.NoError(t, c.Execute(), out.String())
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
