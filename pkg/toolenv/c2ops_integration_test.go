package toolenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/colony-2/c2j/internal/nixtest"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/stretchr/testify/require"
)

// This is the complete unchanged c2ops hook environment, not a Node-only
// probe. Route acceptance must additionally run the actual complete hook.
func TestRealC2opsEnvironment(t *testing.T) {
	fixture := "testdata/c2ops-tools.json"
	if os.Getenv("C2J_TEST_C2OPS_LOCAL") != "1" {
		if !nixtest.InContainer(t, fixture) {
			return
		}
		fixture = "c2ops-tools.json"
	}
	require.Equal(t, "linux/arm64", runtime.GOOS+"/"+runtime.GOARCH, "c2ops acceptance requires Linux aarch64")
	raw, err := os.ReadFile(fixture)
	require.NoError(t, err)
	var declarations struct {
		HookRevision string   `json:"hook_revision"`
		Packages     []string `json:"packages"`
	}
	require.NoError(t, json.Unmarshal(raw, &declarations))
	require.Equal(t, "36c8a3c70cfa8179e445115e635820c97675f4eb", declarations.HookRevision)
	require.Len(t, declarations.Packages, 14)
	scopes := []Scope{{ID: "c2ops-hook", Packages: declarations.Packages}}
	m := Manager{Root: t.TempDir()}
	env, diag, err := m.Prepare(t.Context(), scopes)
	if err != nil {
		evidence, e := json.Marshal(diag)
		require.NoError(t, e)
		t.Logf("complete environment preparation failed: %s", evidence)
	}
	require.NoError(t, err)
	require.Len(t, env.Tools, 14)
	require.True(t, env.Ready())
	require.NotNil(t, env.Native)
	require.Equal(t, "prepared", diag.Native.Outcome)
	for i, tool := range env.Tools {
		t.Logf("%s: %s; %d retained outputs; executables=%d", tool.Reference, diag.Tools[i].Outcome, len(tool.Nix.Roots), len(tool.Executables))
	}
	t.Logf("native activation: %s; %d retained input roots", diag.Native.Outcome, len(env.Native.Roots))
	work := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(work, "probe.c"), []byte("#include <stdio.h>\nint main(void) { puts(\"c2ops-native-ok\"); return 0; }\n"), 0600))
	run := func(e Environment) {
		t.Helper()
		// Each declared package must supply its own binding; ambient tools cannot
		// satisfy an absent export. Use the same environment for native execution.
		for i, name := range []string{"python3", "go", "git", "make", "uv", "node", "tar", "gzip", "pnpm", "gcc", "nix", "bash", "ls", "jq"} {
			require.Contains(t, e.Tools[i].Executables, name, declarations.Packages[i])
			args := []string{name, "--version"}
			if name == "go" {
				args = []string{"go", "version"}
			}
			out, stderr, err := process.ExecuteProcess(e.Bind(t.Context()), process.RunRequest{Command: args})
			require.NoError(t, err, "%s: %s", name, stderr)
			require.NotEmpty(t, out, name)
		}
		out, stderr, err := process.ExecuteProcess(e.Bind(t.Context()), process.RunRequest{WorkingDir: work, Shell: "sh", Run: "set -eu; cc probe.c -o probe; ./probe"})
		require.NoError(t, err, string(stderr))
		require.Contains(t, string(out), "c2ops-native-ok")
	}
	run(env)
	t.Setenv("PATH", t.TempDir())
	again, warm, err := m.Prepare(t.Context(), scopes)
	require.NoError(t, err)
	require.True(t, again.Ready())
	require.Equal(t, "reused", warm.Native.Outcome)
	for _, tool := range warm.Tools {
		require.Equal(t, "reused", tool.Outcome)
	}
	run(again)
	t.Logf("all fourteen declarations and native activation prepared; manager-free warm reuse; c2ops hook=%s", declarations.HookRevision)
}
