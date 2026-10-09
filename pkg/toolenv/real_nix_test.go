package toolenv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/stretchr/testify/require"
)

// Run in a Nix-equipped environment with C2J_TEST_REAL_NIX=1. This deliberately
// uses an immutable nixpkgs revision and needs access to its binary cache.
func TestRealNix(t *testing.T) {
	if os.Getenv("C2J_TEST_REAL_NIX") == "" {
		t.Skip("set C2J_TEST_REAL_NIX=1 for the Nix integration test")
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
