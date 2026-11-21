package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/buildinfo"
	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
)

func cliCatalog(scope execution.CatalogScope) execution.BaseCatalog {
	now := time.Now().UTC()
	return execution.BaseCatalog{Version: 1, Scope: scope, Complete: true, NamedBases: []string{"macos-15-arm64", "linux"}, Dynamic: []execution.DynamicBaseRule{{Kind: "oci"}}, Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
}
func TestExecutionBaseCatalogCommands(t *testing.T) {
	isolateCatalogCLI(t)
	t.Setenv(gitprofile.SocketEnv, "")
	t.Setenv(executioncatalog.RequireEnv, "true")
	ctx := celltest.Context(context.Background())
	rt, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "db")})
	require.NoError(t, err)
	defer rt.Close(ctx)
	server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(rt)))
	defer server.Close()
	scope := execution.CatalogScope{Runtime: server.URL, Tenant: "test"}
	c := cliCatalog(scope)
	raw, _ := json.Marshal(c)
	file := filepath.Join(t.TempDir(), "catalog.json")
	require.NoError(t, os.WriteFile(file, raw, 0600))
	_, err = rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "test", Name: "ordinary", Value: json.RawMessage(`"preserved"`)})
	require.NoError(t, err)
	run := func(args ...string) (string, error) {
		root := newRootCmd(buildinfo.Info{})
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(append([]string{"execution", "bases", "--jobdb", server.URL + "/test"}, args...))
		err := root.Execute()
		return out.String(), err
	}
	_, err = run("set", "--file", file)
	require.NoError(t, err)
	out, err := run("get")
	require.NoError(t, err)
	require.Contains(t, out, `"generation":"one"`)
	out, err = run("--json")
	require.NoError(t, err)
	var view executioncatalog.View
	require.NoError(t, json.Unmarshal([]byte(out), &view))
	require.Equal(t, "authoritative", view.Status)
	require.NotEmpty(t, view.Revision)
	require.Equal(t, []string{"linux", "macos-15-arm64"}, view.Catalog.NamedBases)
	snapshot, err := rt.GetTenantConfiguration(ctx, "test")
	require.NoError(t, err)
	require.JSONEq(t, `"preserved"`, string(snapshot.Values["ordinary"]))
	_, err = rt.RemoveTenantConfigurationValue(ctx, jobdb.RemoveTenantConfigurationValueRequest{TenantID: "test", Name: execution.BaseCatalogKey})
	require.NoError(t, err)
	out, err = run("--json")
	require.ErrorContains(t, err, executioncatalog.Unavailable)
	require.Contains(t, out, `"status":"unavailable"`)
	require.NotContains(t, out, `"named_bases":[]`)
	t.Setenv(executioncatalog.RequireEnv, "false")
	out, err = run("--json")
	require.NoError(t, err)
	require.Contains(t, out, "not_configured")
	t.Setenv(gitprofile.SocketEnv, "/does-not-exist")
	for _, op := range []string{"get", "set"} {
		_, err = run(op)
		require.ErrorContains(t, err, "provisioning context")
	}
}

type faultCatalogAdmin struct {
	jobdb.ConfigurationAdmin
	mode   string
	writes int
}

func (a *faultCatalogAdmin) SetTenantConfigurationValue(ctx context.Context, q jobdb.SetTenantConfigurationValueRequest) (string, error) {
	a.writes++
	if a.mode == "conflict" {
		q.Value = json.RawMessage(`{"winner":true}`)
		_, err := a.ConfigurationAdmin.SetTenantConfigurationValue(ctx, q)
		if err != nil {
			return "", err
		}
		return "", jobdb.ErrConfigurationRevisionMismatch
	}
	if a.mode == "lost" {
		_, err := a.ConfigurationAdmin.SetTenantConfigurationValue(ctx, q)
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("lost response")
}
func TestExecutionBaseCatalogConditionalPublication(t *testing.T) {
	for _, mode := range []string{"conflict", "lost", "uncommitted"} {
		t.Run(mode, func(t *testing.T) {
			rt := toy.New()
			scope := execution.CatalogScope{Runtime: "https://test", Tenant: "t"}
			c := cliCatalog(scope)
			raw, _ := json.Marshal(c)
			admin := &faultCatalogAdmin{ConfigurationAdmin: rt, mode: mode}
			revision, err := publishBaseCatalog(context.Background(), admin, scope, raw, time.Now().UTC())
			require.Equal(t, 1, admin.writes)
			if mode == "lost" {
				require.NoError(t, err)
				require.NotEmpty(t, revision)
			} else {
				require.Error(t, err)
			}
			snapshot, e := rt.GetTenantConfiguration(context.Background(), "t")
			require.NoError(t, e)
			if mode == "conflict" {
				require.JSONEq(t, `{"winner":true}`, string(snapshot.Values[execution.BaseCatalogKey]))
			}
		})
	}
}
func TestExecutionBaseCatalogInvalidTargets(t *testing.T) {
	isolateCatalogCLI(t)
	t.Setenv(gitprofile.SocketEnv, "")
	for _, uri := range []string{"http://localhost", "http://user:private@localhost/t", "https://localhost/t?token=private"} {
		root := newRootCmd(buildinfo.Info{})
		root.SetArgs([]string{"execution", "bases", "get", "--jobdb", uri})
		err := root.Execute()
		require.Error(t, err)
		require.NotContains(t, err.Error(), "private")
	}
}

func isolateCatalogCLI(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "C2J_CURRENT_") || strings.HasPrefix(name, "C2J_CHILD_JOB_") {
			t.Setenv(name, "")
		}
	}
}

func TestExecutionBaseCatalogScopedCLIFormats(t *testing.T) {
	isolateCatalogCLI(t)
	c := cliCatalog(execution.CatalogScope{Runtime: "https://test", Tenant: "t"})
	raw, _ := json.Marshal(c)
	p := executioncatalog.Policy{Required: true, Scope: c.Scope, Read: func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		return jobdb.ConfigurationSnapshot{Revision: "fresh", Values: map[string]json.RawMessage{execution.BaseCatalogKey: raw}}, nil
	}}
	snapshot := jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{execution.BaseCatalogKey: raw}}
	scope, err := gitprofile.NewScope(executioncatalog.WithPolicy(context.Background(), p), gitprofile.Selection{}, snapshot, nil, "")
	require.NoError(t, err)
	defer scope.Close()
	for _, env := range gitprofile.Env(scope.Context(), nil) {
		if value, ok := strings.CutPrefix(env, gitprofile.SocketEnv+"="); ok {
			t.Setenv(gitprofile.SocketEnv, value)
		}
	}
	for _, format := range []string{"raw", "json"} {
		root := newRootCmd(buildinfo.Info{})
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{"config", "get", execution.BaseCatalogKey, "--format", format})
		require.ErrorContains(t, root.Execute(), "reserved")
		require.NotContains(t, out.String(), `"named_bases"`)
	}
	t.Setenv(executioncatalog.RequireEnv, "false")
	root := newRootCmd(buildinfo.Info{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"execution", "bases", "--json"})
	require.NoError(t, root.Execute())
	require.Contains(t, out.String(), `"revision":"fresh"`)
	require.Contains(t, out.String(), "macos-15-arm64")
	t.Setenv(gitprofile.SocketEnv, "")
	t.Setenv("C2J_CURRENT_JOB_ID", "current")
	root = newRootCmd(buildinfo.Info{})
	root.SetArgs([]string{"execution", "bases", "get", "--jobdb", "http://user:private@host/t"})
	err = root.Execute()
	require.ErrorContains(t, err, "provisioning context")
	require.NotContains(t, err.Error(), "private")
}
