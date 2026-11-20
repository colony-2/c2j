//go:build integration

package toolenv

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/pnpmtest"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/stretchr/testify/require"
)

func TestPnpmRealDependencyBuilds(t *testing.T) {
	if os.Getenv("C2J_TEST_REAL_PNPM") != "1" {
		t.Skip("set C2J_TEST_REAL_PNPM=1 to exercise the actual bootstrap")
	}
	_, err := exec.LookPath("pnpm")
	require.NoError(t, err, "the required real-manager check must not skip a missing bootstrap")
	dir := t.TempDir()
	transitive := pnpmtest.Archive(t, dir, "c2j-transitive", "1.0.0", pnpmtest.BuildScript, nil, nil)
	optional := pnpmtest.Archive(t, dir, "c2j-optional", "1.0.0", pnpmtest.BuildScript, nil, nil)
	direct := pnpmtest.Archive(t, dir, "c2j-direct", "1.0.0", pnpmtest.BuildScript, map[string]string{"c2j-transitive": transitive}, map[string]string{"c2j-optional": optional})
	unrelated := pnpmtest.Archive(t, dir, "c2j-unrelated", "2.0.0", pnpmtest.BuildScript, nil, nil)
	// Local archives require no registry resolution. Keep consumer files outside
	// the private projects and prove they remain unchanged.
	consumer := filepath.Join(dir, "pnpm-workspace.yaml")
	require.NoError(t, os.WriteFile(consumer, []byte("ignoreScripts: true\n"), 0600))
	t.Setenv("PNPM_CONFIG_REGISTRY", "http://127.0.0.1:1")
	scopes := []Scope{{ID: "authorized", Packages: []string{"pnpm:" + direct, "pnpm:" + unrelated}}}
	m := Manager{Root: t.TempDir()}
	env, diag, err := m.Prepare(context.Background(), scopes)
	require.NoError(t, err)
	require.True(t, env.Ready())
	for _, d := range diag.Tools {
		t.Logf("actual bootstrap: %+v", *d.Pnpm)
	}
	markers := 0
	require.NoError(t, filepath.Walk(m.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Name() == "built" {
			b, e := os.ReadFile(path)
			require.NoError(t, e)
			require.Equal(t, "yes", string(b))
			markers++
		}
		return nil
	}))
	require.Equal(t, 4, markers, "direct, transitive, selected optional and unrelated builds must all run")
	out, stderr, err := runnerCommand(t, env, "pnpm", "--package="+direct, "dlx", "c2j-direct")
	require.NoError(t, err, stderr)
	require.Equal(t, "c2j-direct@1.0.0\n", out)
	b, err := os.ReadFile(consumer)
	require.NoError(t, err)
	require.Equal(t, "ignoreScripts: true\n", string(b))
	again, diag, err := m.Prepare(context.Background(), scopes)
	require.NoError(t, err)
	require.Equal(t, env, again)
	for _, d := range diag.Tools {
		require.Equal(t, "reused", d.Outcome)
	}
	// A required script error remains a setup failure with no ready entry.
	bad := pnpmtest.Archive(t, dir, "c2j-broken", "1.0.0", "node -e \"process.exit(17)\"", nil, nil)
	broken := Manager{Root: t.TempDir()}
	_, diag, err = broken.Prepare(context.Background(), []Scope{{ID: "op", Packages: []string{"pnpm:" + bad}}})
	require.Error(t, err)
	require.Equal(t, "failed", diag.Tools[0].Outcome)
	manifests, e := filepath.Glob(filepath.Join(broken.Root, "packages", "*", "ready.json"))
	require.NoError(t, e)
	require.Empty(t, manifests)
	// Inherited denials are rejected, rather than hidden by our private policy.
	t.Setenv("PNPM_CONFIG_IGNORE_SCRIPTS", "true")
	denied := Manager{Root: t.TempDir()}
	_, _, err = denied.Prepare(context.Background(), scopes)
	require.ErrorContains(t, err, "conflicting pnpm setting ignorescripts")
}

func TestPnpmSeparateProcessPublication(t *testing.T) {
	if root := os.Getenv("C2J_PNPM_CHILD_ROOT"); root != "" {
		env, _, err := (&Manager{Root: root}).Prepare(context.Background(), pnpmScope())
		require.NoError(t, err)
		require.True(t, env.Ready())
		return
	}
	_, log := pnpmFixture(t)
	root := t.TempDir()
	commands := make([]*exec.Cmd, 4)
	for i := range commands {
		c := exec.Command(os.Args[0], "-test.run=^TestPnpmSeparateProcessPublication$")
		c.Env = append(os.Environ(), "C2J_PNPM_CHILD_ROOT="+root)
		commands[i] = c
		require.NoError(t, c.Start())
	}
	for _, c := range commands {
		require.NoError(t, c.Wait())
	}
	b, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, "fixture@1\n", string(b))
	// Exercise deserialization after processes have independently published.
	env, _, err := (&Manager{Root: root}).Prepare(context.Background(), pnpmScope())
	require.NoError(t, err)
	b, err = json.Marshal(env)
	require.NoError(t, err)
	var restored Environment
	require.NoError(t, json.Unmarshal(b, &restored))
	require.True(t, restored.Ready())
}

func executePnpmAt(env Environment, dir string, command []string) (string, string, error) {
	out, stderr, err := process.ExecuteProcess(env.Bind(context.Background()), process.RunRequest{WorkingDir: dir, Command: command})
	return string(out), string(stderr), err
}

// A required worker acceptance run must not turn an unavailable native row
// into a successful suite through a platform skip. Compatibility matrix rows
// can still run the rest of the tests on their own platforms.
func TestPnpmRequiredNativeAllocation(t *testing.T) {
	if os.Getenv("C2J_REQUIRE_NATIVE_PNPM") != "1" {
		return
	}
	require.Equal(t, "1", os.Getenv("C2J_TEST_REAL_PNPM"), "native acceptance requires actual package preparation")
	_, err := pnpmNativePlatform(runtime.GOOS, runtime.GOARCH)
	require.NoError(t, err, "native pnpm 12.10.1 acceptance requires a supported Linux allocation")
	if runtime.GOOS == "linux" {
		maps, err := os.ReadFile("/proc/self/maps")
		require.NoError(t, err, "native acceptance requires process provenance")
		for _, translator := range []string{"/run/rosetta/rosetta", "/qemu-x86_64", "/qemu-aarch64"} {
			require.False(t, strings.Contains(string(maps), translator), "native acceptance failed: process maps identify %s emulation; native execution is required", translator)
		}
	}
}

// Readiness checks package preparation and local reuse. OS-enforced network
// denial is exercised separately by TestPnpmNativeOffline in the verify hook.
func TestPnpmNativeReadiness(t *testing.T) {
	if os.Getenv("C2J_TEST_REAL_PNPM") != "1" {
		t.Skip("set C2J_TEST_REAL_PNPM=1 for actual payload readiness")
	}
	platform, err := pnpmNativePlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		if os.Getenv("C2J_REQUIRE_NATIVE_PNPM") == "1" {
			require.NoError(t, err)
		}
		t.Skip(err.Error())
	}
	m := Manager{Root: t.TempDir()}
	scopes := []Scope{{ID: "native", Packages: []string{"pnpm:pnpm@12.10.1"}}}
	env, diag, err := m.Prepare(context.Background(), scopes)
	require.NoError(t, err)
	require.True(t, env.Ready())
	require.Equal(t, platform.Integrity, env.Tools[0].Pnpm.Native.NativeIntegrity)
	require.Equal(t, platform.Machine, diag.Tools[0].Pnpm.Machine)
	t.Logf("readiness platform: %s/%s; validated payload: %+v", runtime.GOOS, runtime.GOARCH, *env.Tools[0].Pnpm.Native)
	originalPath := os.Getenv("PATH")
	traps := t.TempDir()
	marker := filepath.Join(traps, "unexpected-call")
	for _, name := range []string{"pnpm", "node"} {
		require.NoError(t, os.WriteFile(filepath.Join(traps, name), []byte("#!/bin/sh\necho called >> \"$C2J_PNPM_TRAP_LOG\"\nexit 99\n"), 0700))
	}
	t.Setenv("C2J_PNPM_TRAP_LOG", marker)
	t.Setenv("PATH", traps+string(os.PathListSeparator)+originalPath)
	again, warmDiag, err := m.Prepare(context.Background(), scopes)
	require.NoError(t, err)
	require.Equal(t, env, again)
	require.Equal(t, "reused", warmDiag.Tools[0].Outcome)
	neutral := t.TempDir()
	for _, command := range [][]string{{"pnpm", "--version"}, {"pn", "--version"}, {"pnpm", "--package=pnpm@12.10.1", "dlx", "pnpm", "--version"}, {"sh", "-c", "pnpm --version"}} {
		out, stderr, err := executePnpmAt(env, neutral, command)
		require.NoError(t, err, stderr)
		require.Equal(t, pnpmVersion+"\n", out)
	}
	for _, alias := range []string{"pnpx", "pnx"} {
		out, stderr, err := executePnpmAt(env, neutral, []string{alias, "--help"})
		require.NoError(t, err, stderr)
		require.NotEmpty(t, out)
	}
	_, err = os.Stat(marker)
	require.True(t, os.IsNotExist(err), "warm preparation and execution must not use the bootstrap or Node")
	require.NoError(t, os.Remove(filepath.Join(env.Tools[0].Bin, "pn")))
	require.False(t, env.Ready(), "missing alias must invalidate cached readiness")
	t.Setenv("PATH", originalPath)
	repaired, repairDiag, err := m.Prepare(context.Background(), scopes)
	require.NoError(t, err)
	require.True(t, repaired.Ready())
	require.NotEqual(t, "reused", repairDiag.Tools[0].Outcome)
	t.Log("cold preparation, warm bootstrap traps, ordinary/qualified/alias execution and damaged-cache repair passed; no OS network-isolation claim")
}
