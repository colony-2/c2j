package toolenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/stretchr/testify/require"
)

// The parent launches a disposable official Nix container. The child exercises
// the real installer using an immutable nixpkgs revision and its binary cache.
func TestRealNix(t *testing.T) {
	if testing.Short() {
		t.Skip("Nix integration test requires Docker and network access")
	}
	if os.Getenv("C2J_NIX_TEST_CONTAINER") != "1" {
		runNixTestContainer(t)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// Setup must override ambient settings that would otherwise permit builds.
	t.Setenv("NIX_CONFIG", "max-jobs = 1\nbuilders = ssh://c2j-no-build.invalid\n")
	const spec = "github:NixOS/nixpkgs/b6018f87da91d19d0ab4cf979885689b469cdd41#findutils"
	m := Manager{Root: t.TempDir()}
	scopes := []Scope{{ID: "op", Packages: []string{"nix:" + spec}}}
	env, diag, err := m.Prepare(ctx, scopes)
	require.NoError(t, err)
	require.True(t, env.Ready())
	require.Equal(t, "prepared", diag.Tools[0].Outcome)
	require.Equal(t, "find", env.Tools[0].Main)
	t.Logf("cold setup: %d ms", diag.WallMS)
	for _, command := range [][]string{{"find", "--version"}, {"nix", "run", spec, "--", "--version"}} {
		out, stderr, err := process.ExecuteProcess(process.WithToolPath(ctx, env.Path), process.RunRequest{Command: command})
		require.NoError(t, err, string(stderr))
		require.Contains(t, string(out), "GNU findutils")
	}
	// An empty PATH makes any accidental manager call on a warm hit fail.
	t.Run("warm_without_managers", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		again, diag, err := m.Prepare(ctx, scopes)
		require.NoError(t, err)
		require.Equal(t, env, again)
		require.Equal(t, "reused", diag.Tools[0].Outcome)
		t.Logf("warm setup: %d ms", diag.WallMS)
	})
	for _, preferLocal := range []bool{false, true} {
		t.Run(fmt.Sprintf("uncached_preferLocalBuild_%t", preferLocal), func(t *testing.T) {
			dir := t.TempDir()
			system := map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH] + "-" + runtime.GOOS
			flake := fmt.Sprintf(`{
  outputs = { self }: {
    packages.%s.default = builtins.derivation {
      name = "c2j-must-not-build";
      system = "%s";
      builder = "/bin/sh";
      args = [ "-c" "echo SOURCE_BUILD_ATTEMPTED >&2; exit 99" ];
      preferLocalBuild = %t;
      allowSubstitutes = false;
    };
  };
}`, system, system, preferLocal)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "flake.nix"), []byte(flake), 0600))
			missing := Manager{Root: t.TempDir()}
			_, diag, err := missing.Prepare(ctx, []Scope{{ID: "op", Packages: []string{"nix:path:" + dir}}})
			require.Error(t, err)
			require.Contains(t, err.Error(), "max-jobs")
			require.NotContains(t, err.Error(), "SOURCE_BUILD_ATTEMPTED")
			require.Equal(t, "failed", diag.Tools[0].Outcome)
			manifests, err := filepath.Glob(filepath.Join(missing.Root, "packages", "*", "ready.json"))
			require.NoError(t, err)
			require.Empty(t, manifests)
		})
	}
}

func runNixTestContainer(t *testing.T) {
	t.Helper()
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Nix integration test: Docker is not installed")
	}
	probeCtx, cancelProbe := context.WithTimeout(t.Context(), 15*time.Second)
	out, err := exec.CommandContext(probeCtx, docker, "info", "--format", "{{.OSType}}/{{.Architecture}}").CombinedOutput()
	cancelProbe()
	if err != nil {
		t.Skipf("Nix integration test: Docker is unavailable: %v: %s", err, out)
	}
	var arch string
	switch strings.TrimSpace(string(out)) {
	case "linux/aarch64", "linux/arm64":
		arch = "arm64"
	case "linux/x86_64", "linux/amd64":
		arch = "amd64"
	default:
		t.Skipf("Nix integration test: unsupported Docker platform %s", out)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "toolenv.test")
	// A static Linux binary also works with a remote daemon or Docker Desktop.
	build := exec.CommandContext(ctx, "go", "test", "-c", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	out, err = build.CombinedOutput()
	require.NoError(t, err, string(out))
	run := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, docker, args...).CombinedOutput()
		require.NoError(t, err, "docker %v: %s", args, out)
		return out
	}
	cidFile := filepath.Join(dir, "container-id")
	run("create", "--cidfile", cidFile, "-e", "C2J_NIX_TEST_CONTAINER=1",
		"--entrypoint", "/tmp/toolenv.test", "nixos/nix:2.35.1",
		"-test.run", "^TestRealNix$", "-test.v", "-test.timeout", "5m")
	cid, err := os.ReadFile(cidFile)
	require.NoError(t, err)
	id := strings.TrimSpace(string(cid))
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(cleanupCtx, docker, "rm", "-f", id).CombinedOutput()
		if err != nil {
			t.Errorf("remove Nix test container: %v: %s", err, out)
		}
	})
	// Copy instead of bind mounting: the daemon need not share the host filesystem.
	run("cp", binary, id+":/tmp/toolenv.test")
	t.Logf("%s", run("start", "--attach", id))
	require.Equal(t, "0", strings.TrimSpace(string(run("inspect", "--format", "{{.State.ExitCode}}", id))), "Nix container tests failed")
}
