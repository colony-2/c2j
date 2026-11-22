package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

func TestExecutionBaseCatalogCLIProcess(t *testing.T) {
	binary := os.Getenv("C2J_TEST_EXECUTION_BASES_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "c2j")
		command := exec.Command("go", "build", "-mod=readonly", "-o", binary, ".")
		out, err := command.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	ctx := celltest.Context(context.Background())
	rt, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "db")})
	require.NoError(t, err)
	defer rt.Close(ctx)
	server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(rt)))
	defer server.Close()
	scope := execution.CatalogScope{Runtime: server.URL, Tenant: "test"}
	run := func(env []string, args ...string) (string, string, int) {
		t.Helper()
		call, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(call, binary, append([]string{"execution", "bases"}, args...)...)
		for _, entry := range os.Environ() {
			name, _, _ := strings.Cut(entry, "=")
			if !strings.HasPrefix(name, "C2J_") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, env...)
		command.Dir = t.TempDir()
		var out, diag bytes.Buffer
		command.Stdout = &out
		command.Stderr = &diag
		err := command.Run()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			code = exit.ExitCode()
		}
		return out.String(), diag.String(), code
	}
	admin := []string{"C2J_JOBDB=" + server.URL + "/test"}
	ok := func(args ...string) string {
		t.Helper()
		out, diag, code := run(admin, args...)
		require.Zero(t, code, diag)
		require.Empty(t, diag)
		return out
	}
	snapshot := func() jobdb.ConfigurationSnapshot {
		t.Helper()
		s, err := rt.GetTenantConfiguration(ctx, "test")
		require.NoError(t, err)
		return s
	}
	t.Run("E01_E02_focused_session", func(t *testing.T) {
		for _, command := range [][]string{{"--help"}, {"capability", "--help"}, {"ranges", "set", "--help"}} {
			out := ok(command...)
			require.NotEmpty(t, out)
			require.NotContains(t, out, "dynamic")
		}
		ok("init", "--valid-for", "24h")
		ok("add", "linux", "--capability", "docker", "--cpu-min", "500m", "--cpu-max", "8", "--memory-min", "1Gi", "--memory-max", "32Gi")
		ok("add", "macos", "--cpu-not-allowed", "--memory-unbounded")
		ok("capability", "add", "linux", "docker.workspace-mounts")
		ok("ranges", "set", "linux", "--cpu-max", "16")
		out := ok("get")
		if destination := os.Getenv("C2J_TEST_EXECUTION_BASES_CAPTURE"); destination != "" {
			require.NoError(t, os.MkdirAll(destination, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(destination, "offerings.txt"), []byte("$ c2j execution bases get\n"+out), 0600))
		}
		require.Contains(t, out, "not allowed")
		require.Contains(t, out, "docker.workspace-mounts")
		require.Contains(t, out, "500m–16")
		out = ok("check", "linux", "--capability", "docker", "--cpu", "16", "--memory", "32Gi", "--json")
		var result map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &result))
		require.Equal(t, "allowed", result["decision"])
		before := snapshot()
		out, diag, code := run(admin, "check", "linux", "--cpu", "17", "--json")
		require.NotZero(t, code)
		require.NotEmpty(t, diag)
		require.NoError(t, json.Unmarshal([]byte(out), &result))
		require.Equal(t, "not_allowed", result["decision"])
		if destination := os.Getenv("C2J_TEST_EXECUTION_BASES_CAPTURE"); destination != "" {
			plain, diagnostic, status := run(admin, "check", "linux", "--capability", "missing", "--cpu", "17", "--memory", "64Gi")
			require.NotZero(t, status)
			require.NoError(t, os.WriteFile(filepath.Join(destination, "refused-request.txt"), []byte("$ c2j execution bases check linux --capability missing --cpu 17 --memory 64Gi\n"+plain+diagnostic), 0600))
		}
		require.Equal(t, before, snapshot())
		ok("capability", "remove", "linux", "docker")
		ok("capability", "clear", "linux")
		ok("remove", "macos")
	})
	t.Run("E01_C13_helper_and_denials", func(t *testing.T) {
		p := executioncatalog.Policy{Required: true, Scope: scope, Read: func(ctx context.Context) (jobdb.ConfigurationSnapshot, error) {
			return rt.GetTenantConfiguration(ctx, "test")
		}}
		helper, err := gitprofile.NewScope(executioncatalog.WithPolicy(ctx, p), gitprofile.Selection{}, snapshot(), nil, "")
		require.NoError(t, err)
		defer helper.Close()
		env := gitprofile.Env(helper.Context(), nil)
		out, diag, code := run(env, "get", "linux", "--json")
		require.Zero(t, code, diag)
		var v executioncatalog.View
		require.NoError(t, json.Unmarshal([]byte(out), &v))
		require.True(t, v.Filtered)
		out, diag, code = run(env, "check", "linux", "--cpu", "1", "--json")
		require.Zero(t, code, diag)
		require.Contains(t, out, `"decision":"allowed"`)
		before := snapshot()
		for _, args := range [][]string{{"set", "--file", "/does-not-exist", "--json"}, {"init", "--valid-for", "1h", "--json"}, {"get", "--jobdb", "http://user:private@host/t", "--json"}, {"check", "linux", "--embed", "--json"}} {
			out, diag, code = run(env, args...)
			require.NotZero(t, code)
			require.NotContains(t, out+diag, "private")
			require.NotContains(t, out+diag, "no such file")
			require.True(t, json.Valid([]byte(out)), out)
		}
		require.Equal(t, before, snapshot())
		helper.Close()
		out, _, code = run(env, "check", "linux", "--json")
		require.NotZero(t, code)
		require.Contains(t, out, `"decision":"unavailable"`)
	})
	t.Run("E01_unavailable_authority", func(t *testing.T) {
		_, err := rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "test", Name: execution.BaseCatalogKey, Value: json.RawMessage(`{}`)})
		require.NoError(t, err)
		out, _, code := run(admin, "check", "linux", "--json")
		require.NotZero(t, code)
		require.Contains(t, out, `"decision":"unavailable"`)
	})
}
