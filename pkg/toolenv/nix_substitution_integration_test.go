package toolenv

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/nixtest"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/stretchr/testify/require"
)

func TestRealNixSubstitution(t *testing.T) {
	if !nixtest.InSubstitutionConsumer(t, "testdata/nix-substitution", []string{"cached", "missing", "untrusted", "missing_closure", "ifd"}) {
		return
	}
	f := nixtest.SignedCache(t)
	mode := os.Getenv("C2J_NIX_SUBSTITUTION_CASE")
	m := Manager{Root: t.TempDir()}
	spec := "path:/tmp/nix-substitution"
	if mode == "ifd" {
		spec += "#forbidden"
	}
	scopes := []Scope{{ID: "op", Packages: []string{"nix:" + spec}}}
	if mode == "cached" {
		// Instantiate the same derivation in the consumer without realizing any
		// outputs. Otherwise a missing .drv would make the old-request control
		// fail for the wrong reason.
		raw, err := runNix(t.Context(), t.TempDir(), "eval", "--json", "--apply", nixToolMetadata, "--", spec)
		require.NoError(t, err)
		require.Contains(t, string(raw), f.Drv)
		// Prove that the old request really fails with the same signed cache.
		_, err = runNix(t.Context(), t.TempDir(), "build", "--no-link", "--", f.Drv+"^out,dev,bin,lib")
		require.ErrorContains(t, err, "max-jobs")
		require.Contains(t, err.Error(), f.Drv)
		require.NotContains(t, err.Error(), "dependency failed", "control must reject the wrapper itself")
		for _, path := range f.Paths {
			_, err := os.Stat(path)
			require.True(t, os.IsNotExist(err), "control must not prewarm selected outputs")
		}
		nixtest.AssertNoBuilders(t)
	}
	env, diag, err := m.Prepare(t.Context(), scopes)
	if mode != "cached" {
		require.Error(t, err)
		require.NotEmpty(t, diag.Error)
		require.Equal(t, "failed", diag.Tools[0].Outcome)
		require.False(t, env.Ready())
		if mode == "untrusted" {
			require.Contains(t, err.Error(), "not signed by any of the keys in 'trusted-public-keys'")
		}
		if mode == "ifd" {
			require.Contains(t, err.Error(), "allow-import-from-derivation")
		}
		manifests, e := filepath.Glob(filepath.Join(m.Root, "packages", "*", "ready.json"))
		require.NoError(t, e)
		require.Empty(t, manifests)
		t.Logf("expected %s rejection: %s", mode, diag.Error)
		nixtest.AssertNoBuilders(t)
		return
	}
	require.NoError(t, err)
	require.True(t, env.Ready())
	require.Equal(t, "prepared", diag.Tools[0].Outcome)
	require.Len(t, env.Tools[0].Nix.Roots, 4)
	var retained []string
	for _, root := range env.Tools[0].Nix.Roots {
		retained = append(retained, root.Target)
	}
	require.ElementsMatch(t, f.Paths, retained)
	run := func(e Environment) {
		t.Helper()
		out, stderr, err := process.ExecuteProcess(e.Bind(t.Context()), process.RunRequest{Command: []string{"c2j-wrapper"}})
		require.NoError(t, err, string(stderr))
		require.Equal(t, "signed-closure-ok\n", string(out))
	}
	run(env)
	// The aliased lib/dev paths share one realization/root without losing bin.
	alias, _, err := m.prepareTool(t.Context(), "nix:"+spec+"#alias")
	require.NoError(t, err)
	require.Len(t, alias.Nix.Roots, 3)
	// A real unrooted object proves that this is an effective collection, not
	// a keep-outputs store or accidentally retained producer roots.
	sentinelDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sentinelDir, "sentinel"), []byte("unrooted"), 0600))
	raw, err := runNix(t.Context(), t.TempDir(), "store", "add-path", "--name", "c2j-unrooted-sentinel", sentinelDir)
	require.NoError(t, err)
	sentinel := strings.TrimSpace(string(raw))
	_, err = runNix(t.Context(), t.TempDir(), "store", "gc")
	require.NoError(t, err)
	_, err = os.Stat(sentinel)
	require.True(t, os.IsNotExist(err), "unrooted sentinel must be collected")
	for _, path := range append(append([]string(nil), f.Paths...), f.Payload) {
		_, err := os.Stat(path)
		require.NoError(t, err, "selected output/closure survived GC: %s", path)
	}
	run(env)
	// Repair an incomplete entry while the cache and normal managers exist.
	require.NoError(t, os.Remove(env.Tools[0].Nix.Roots[0].Link))
	require.False(t, env.Ready())
	env, diag, err = m.Prepare(t.Context(), scopes)
	require.NoError(t, err)
	require.Equal(t, "prepared", diag.Tools[0].Outcome)
	f.Server.Close()
	t.Setenv("PATH", t.TempDir())
	again, diag, err := m.Prepare(t.Context(), scopes)
	require.NoError(t, err)
	require.True(t, again.Ready())
	require.Equal(t, "reused", diag.Tools[0].Outcome)
	require.Equal(t, "reused", diag.Native.Outcome)
	run(again)
	nixtest.AssertNoBuilders(t)
	sort.Strings(retained)
	t.Logf("signed outputs retained after GC; offline warm reuse: %v; payload=%s", retained, f.Payload)
}
