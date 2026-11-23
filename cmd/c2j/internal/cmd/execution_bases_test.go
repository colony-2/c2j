package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/colony-2/c2j/internal/executiontest"
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
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func cliCatalog(scope execution.CatalogScope) execution.BaseCatalog {
	now := time.Now().UTC()
	return execution.BaseCatalog{Version: 1, Scope: scope, Complete: true, Offerings: executiontest.Offerings("macos-15-arm64", "linux"), Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
}
func TestExecutionBaseCatalogCommands(t *testing.T) {
	isolateCatalogCLI(t)
	t.Setenv(gitprofile.SocketEnv, "")
	t.Setenv(executioncatalog.RequireEnv, "true")
	ctx := celltest.Context(context.Background())
	rt, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
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
	out, err := run("get", "--json")
	require.NoError(t, err)
	require.Contains(t, out, `"status":"authoritative"`)
	out, err = run("--json")
	require.NoError(t, err)
	var view executioncatalog.View
	require.NoError(t, json.Unmarshal([]byte(out), &view))
	require.Equal(t, "authoritative", view.Status)
	require.NotEmpty(t, view.Revision)
	require.Equal(t, []string{"linux", "macos-15-arm64"}, view.Catalog.Names())
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
	for _, op := range []string{"set", "init", "renew"} {
		_, err = run(op)
		require.ErrorContains(t, err, "provisioning context")
	}
}

type faultCatalogAdmin struct {
	jobdb.ConfigurationAdmin
	mode   string
	writes int
}

func (a *faultCatalogAdmin) GetTenantConfiguration(ctx context.Context, tenant string) (jobdb.ConfigurationSnapshot, error) {
	if a.mode == "readback-failed" {
		return jobdb.ConfigurationSnapshot{}, errors.New("private transport credentials")
	}
	return a.ConfigurationAdmin.GetTenantConfiguration(ctx, tenant)
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
	for _, mode := range []string{"conflict", "lost", "uncommitted", "readback-failed"} {
		t.Run(mode, func(t *testing.T) {
			rt := toy.New(toy.WithProjects(jobdbcelltest.UnrestrictedProjects{}))
			scope := execution.CatalogScope{Runtime: "https://test", Tenant: "t"}
			c := cliCatalog(scope)
			raw, _ := json.Marshal(c)
			admin := &faultCatalogAdmin{ConfigurationAdmin: rt, mode: mode}
			snapshot, e := rt.GetTenantConfiguration(context.Background(), scope.Tenant)
			require.NoError(t, e)
			revision, err := publishBaseCatalogSnapshot(context.Background(), admin, scope, raw, snapshot.Revision)
			require.Equal(t, 1, admin.writes)
			if mode == "lost" {
				require.NoError(t, err)
				require.NotEmpty(t, revision)
			} else {
				require.Error(t, err)
				if mode != "conflict" {
					var uncertain *catalogPublicationUncertain
					require.ErrorAs(t, err, &uncertain)
					require.Equal(t, raw, uncertain.Candidate)
				}
			}
			snapshot, e = rt.GetTenantConfiguration(context.Background(), "t")
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
	require.ErrorContains(t, err, "target overrides")
	require.NotContains(t, err.Error(), "private")
}

func TestExecutionBaseCatalogFocusedEdits(t *testing.T) {
	isolateCatalogCLI(t)
	t.Setenv(gitprofile.SocketEnv, "")
	t.Setenv(executioncatalog.RequireEnv, "false")
	ctx := celltest.Context(context.Background())
	rt, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, err)
	defer rt.Close(ctx)
	server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(rt)))
	defer server.Close()
	_, err = rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "test", Name: "ordinary", Value: json.RawMessage(`"preserved"`)})
	require.NoError(t, err)
	run := func(args ...string) (string, error) {
		t.Helper()
		root := newRootCmd(buildinfo.Info{})
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{"execution", "bases", "--jobdb", server.URL + "/test"}, args...))
		err := root.Execute()
		return out.String(), err
	}
	success := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		require.NoError(t, err, out)
		return out
	}
	snapshot := func() jobdb.ConfigurationSnapshot {
		t.Helper()
		s, err := rt.GetTenantConfiguration(ctx, "test")
		require.NoError(t, err)
		require.JSONEq(t, `"preserved"`, string(s.Values["ordinary"]))
		return s
	}
	current := func() execution.BaseCatalog {
		t.Helper()
		s := snapshot()
		c, err := execution.DecodeBaseCatalogStructure(s.Values[execution.BaseCatalogKey], execution.CatalogScope{Runtime: server.URL, Tenant: "test"})
		require.NoError(t, err)
		return c
	}
	noWrite := func(args ...string) {
		t.Helper()
		before := snapshot()
		out, err := run(args...)
		require.Error(t, err, out)
		require.Equal(t, before, snapshot())
	}
	t.Run("C01_C07_init_add", func(t *testing.T) {
		noWrite("init")
		noWrite("init", "--valid-for", "-1h")
		out := success("init", "--valid-for", "24h", "--json")
		var r catalogReceipt
		require.NoError(t, json.Unmarshal([]byte(out), &r))
		require.True(t, r.Changed)
		require.NotEmpty(t, r.Generation)
		require.Empty(t, current().Offerings)
		noWrite("init", "--valid-for", "24h")
		success("add", "linux", "--capability", "docker", "--capability", "docker", "--cpu-min", "500m", "--cpu-max", "8", "--memory-min", "1Gi", "--memory-max", "32Gi")
		c := current()
		require.Len(t, c.Offerings, 1)
		require.Equal(t, []string{"docker"}, c.Offerings[0].Capabilities)
	})
	expiry := current().ExpiresAt
	t.Run("C02_named_offerings", func(t *testing.T) {
		success("add", "linux", "--offer", "kvm", "--capability", "virtualization.kvm", "--cpu-unbounded", "--memory-not-allowed")
		success("add", "macos", "--cpu-not-allowed", "--memory-unbounded")
		require.Len(t, current().Offerings, 3)
		success("remove", "linux", "--offer", "kvm")
		require.Len(t, current().Offerings, 2)
		success("add", "linux", "--offer", "kvm", "--cpu-unbounded", "--memory-unbounded")
	})
	t.Run("C03_C04_capabilities_and_noops", func(t *testing.T) {
		before := current()
		success("capability", "add", "linux", "docker.workspace-mounts")
		c := current()
		require.Equal(t, before.Offerings[0].Resources, c.Offerings[0].Resources)
		require.Equal(t, before.Offerings[1:], c.Offerings[1:])
		previous := snapshot()
		out := success("capability", "add", "linux", "docker", "--json")
		require.Contains(t, out, `"changed":false`)
		require.Equal(t, previous, snapshot())
		success("capability", "remove", "linux", "docker")
		success("capability", "clear", "linux")
		require.Empty(t, current().Offerings[0].Capabilities)
		previous = snapshot()
		success("capability", "clear", "linux")
		require.Equal(t, previous, snapshot())
		success("capability", "add", "linux", "docker")
	})
	t.Run("C05_C06_range_patches", func(t *testing.T) {
		success("ranges", "set", "linux", "--cpu-max", "16")
		require.Equal(t, "500m", *current().Offerings[0].Resources.CPU.Min)
		success("ranges", "set", "linux", "--cpu-max", "none")
		require.Nil(t, current().Offerings[0].Resources.CPU.Max)
		success("ranges", "set", "linux", "--cpu-min", "none")
		require.Equal(t, "unbounded", current().Offerings[0].Resources.CPU.State)
		success("ranges", "set", "linux", "--cpu-not-allowed")
		require.Nil(t, current().Offerings[0].Resources.CPU.Min)
		success("ranges", "set", "linux", "--cpu-max", "8")
		require.Equal(t, "range", current().Offerings[0].Resources.CPU.State)
		require.Nil(t, current().Offerings[0].Resources.CPU.Min)
		noWrite("ranges", "set", "linux", "--cpu-min", "9")
		noWrite("ranges", "set", "linux", "--cpu-max", "1", "--cpu-not-allowed")
		noWrite("ranges", "set", "linux", "--memory-unbounded", "--memory-not-allowed")
		noWrite("ranges", "set", "linux")
		noWrite("ranges", "set", "absent", "--cpu-unbounded")
		noWrite("add", "new", "--cpu-unbounded")
		noWrite("add", "oci:alpine", "--cpu-unbounded", "--memory-unbounded")
		noWrite("add", "lin*", "--cpu-unbounded", "--memory-unbounded")
	})
	t.Run("C08_preserve_expiry_and_renew", func(t *testing.T) {
		require.Equal(t, expiry, current().ExpiresAt)
		success("renew", "--valid-for", "48h")
		require.True(t, current().ExpiresAt.After(expiry))
		c := current()
		c.PublishedAt = time.Now().UTC().Add(-2 * time.Hour)
		c.ExpiresAt = time.Now().UTC().Add(-time.Hour)
		raw, _ := json.Marshal(c)
		_, err = rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "test", Name: execution.BaseCatalogKey, Value: raw})
		require.NoError(t, err)
		out, err := run("get", "--json")
		require.Error(t, err)
		require.Contains(t, out, `"status":"expired"`)
		noWrite("capability", "add", "linux", "other")
		success("renew", "--valid-for", "24h")
		require.Equal(t, c.Offerings, current().Offerings)
	})
	t.Run("C09_C10_C11_views_import", func(t *testing.T) {
		plain := success()
		require.Contains(t, plain, "not allowed")
		require.Contains(t, plain, "ALLOWED CAPABILITIES")
		require.Contains(t, plain, "Unlisted bases/capabilities")
		require.Equal(t, plain, success("get"))
		require.Equal(t, success("--json"), success("get", "--json"))
		var v executioncatalog.View
		require.NoError(t, json.Unmarshal([]byte(success("get", "linux", "--json")), &v))
		require.True(t, v.Filtered)
		require.False(t, v.Catalog.Complete)
		require.Len(t, v.Catalog.Offerings, 2)
		file := filepath.Join(t.TempDir(), "import.json")
		raw, _ := json.Marshal(v.Catalog)
		require.NoError(t, os.WriteFile(file, raw, 0600))
		noWrite("set", "--file", file)
		noWrite("get", "unknown", "--json")
		c := current()
		c.Generation = "ignored-metadata"
		c.PublishedAt = time.Now().UTC().Add(-time.Second)
		raw, _ = json.Marshal(c)
		require.NoError(t, os.WriteFile(file, raw, 0600))
		before := snapshot()
		success("set", "--file", file)
		require.Equal(t, before, snapshot())
		for _, bad := range []string{`{}`, `{"version":1,"offerings":null}`, strings.Repeat(" ", execution.BaseCatalogMaxBytes+1)} {
			require.NoError(t, os.WriteFile(file, []byte(bad), 0600))
			noWrite("set", "--file", file)
		}
	})
	t.Run("C12_C14_full_checks_zero_writes", func(t *testing.T) {
		before := snapshot()
		success("check", "linux", "--capability", "docker", "--cpu", "8", "--memory", "32Gi", "--json")
		out, err := run("check", "linux", "--capability", "missing", "--cpu", "9", "--memory", "33Gi", "--json")
		require.Error(t, err)
		var result execution.CatalogEvaluation
		require.NoError(t, json.Unmarshal([]byte(out), &result))
		require.Equal(t, "not_allowed", result.Decision)
		require.Len(t, result.Reasons, 4)
		out, err = run("check", "linux", "--cpu", "0", "--json")
		require.Error(t, err)
		require.NoError(t, json.Unmarshal([]byte(out), &result))
		require.Equal(t, "invalid_request", result.Decision)
		require.Equal(t, before, snapshot())
	})
	t.Run("C02_remove_group", func(t *testing.T) {
		success("remove", "linux")
		require.Len(t, current().Offerings, 1)
		require.Equal(t, "macos", current().Offerings[0].Base)
		noWrite("remove", "linux")
	})
}

// Interleave an unrelated edit after reading. The stale candidate must conflict;
// refreshing the expected revision would silently erase the winning catalog.
func TestExecutionBaseCatalogObservedSnapshotConflict(t *testing.T) {
	rt := toy.New(toy.WithProjects(jobdbcelltest.UnrestrictedProjects{}))
	ctx := context.Background()
	scope := execution.CatalogScope{Runtime: "https://test", Tenant: "t"}
	c := cliCatalog(scope)
	raw, _ := json.Marshal(c)
	_, err := rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "t", Name: execution.BaseCatalogKey, Value: raw})
	require.NoError(t, err)
	snapshot, err := rt.GetTenantConfiguration(ctx, "t")
	require.NoError(t, err)
	winner := cliCatalog(scope)
	winner.Generation = "winner"
	winner.Offerings = executiontest.Offerings("winner")
	raw, _ = json.Marshal(winner)
	_, err = rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "t", Name: execution.BaseCatalogKey, Value: raw})
	require.NoError(t, err)
	candidate := c
	candidate.Generation = "stale"
	raw, _ = json.Marshal(candidate)
	_, err = publishBaseCatalogSnapshot(ctx, rt, scope, raw, snapshot.Revision)
	require.ErrorIs(t, err, jobdb.ErrConfigurationRevisionMismatch)
	after, err := rt.GetTenantConfiguration(ctx, "t")
	require.NoError(t, err)
	require.Contains(t, string(after.Values[execution.BaseCatalogKey]), "winner")
	require.NotContains(t, string(after.Values[execution.BaseCatalogKey]), "stale")
}

type advancingCatalogAdmin struct {
	jobdb.ConfigurationAdmin
	winner        []byte
	reads, writes int
}

func (a *advancingCatalogAdmin) GetTenantConfiguration(ctx context.Context, tenant string) (jobdb.ConfigurationSnapshot, error) {
	a.reads++
	snapshot, err := a.ConfigurationAdmin.GetTenantConfiguration(ctx, tenant)
	if err != nil {
		return snapshot, err
	}
	if a.reads == 1 {
		_, err = a.ConfigurationAdmin.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: tenant, Name: execution.BaseCatalogKey, Value: a.winner})
	}
	return snapshot, err
}
func (a *advancingCatalogAdmin) SetTenantConfigurationValue(ctx context.Context, request jobdb.SetTenantConfigurationValueRequest) (string, error) {
	a.writes++
	return a.ConfigurationAdmin.SetTenantConfigurationValue(ctx, request)
}
func TestExecutionBaseCatalogMutationSnapshotConflict(t *testing.T) {
	rt := toy.New(toy.WithProjects(jobdbcelltest.UnrestrictedProjects{}))
	ctx := context.Background()
	scope := execution.CatalogScope{Runtime: "https://test", Tenant: "t"}
	c := cliCatalog(scope)
	raw, _ := json.Marshal(c)
	_, err := rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "t", Name: execution.BaseCatalogKey, Value: raw})
	require.NoError(t, err)
	winner := cliCatalog(scope)
	winner.Generation = "winner"
	winner.Offerings = executiontest.Offerings("winner")
	raw, _ = json.Marshal(winner)
	admin := &advancingCatalogAdmin{ConfigurationAdmin: rt, winner: raw}
	command := &cobra.Command{Use: "add"}
	_, err = editBaseCatalog(ctx, admin, scope, command, nil, nil, func(_ *cobra.Command, _ []string, c *execution.BaseCatalog) error {
		c.Offerings[0].Capabilities = append(c.Offerings[0].Capabilities, "special")
		return nil
	}, time.Now().UTC())
	require.ErrorIs(t, err, jobdb.ErrConfigurationRevisionMismatch)
	require.Equal(t, 1, admin.reads)
	require.Equal(t, 1, admin.writes)
	after, err := rt.GetTenantConfiguration(ctx, "t")
	require.NoError(t, err)
	require.JSONEq(t, string(raw), string(after.Values[execution.BaseCatalogKey]))
}
