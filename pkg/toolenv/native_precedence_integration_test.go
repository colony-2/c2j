package toolenv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/nixtest"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/stretchr/testify/require"
)

func TestRealNativeLibraryPrecedence(t *testing.T) {
	if !nixtest.InContainer(t) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	system, err := NixSystem()
	require.NoError(t, err)
	fixture := t.TempDir()
	flake := fmt.Sprintf(`{
  inputs.nixpkgs.url = "%s";
  outputs = { self, nixpkgs }: let
    pkgs = nixpkgs.legacyPackages.%s;
    probe = value: pkgs.runCommand "scope-probe-${toString value}" {} ''
      mkdir -p $out/include $out/lib/pkgconfig
      echo '#define SCOPE_PROBE_VALUE ${toString value}' > $out/include/scope-probe.h
      echo 'int scope_probe(void);' >> $out/include/scope-probe.h
      echo 'int scope_probe(void) { return ${toString value}; }' > probe.c
      ${pkgs.stdenv.cc}/bin/cc -c probe.c -o probe.o
      ${pkgs.binutils}/bin/ar rcs $out/lib/libscopeprobe.a probe.o
      cat > $out/lib/pkgconfig/scope-probe.pc <<EOF
Name: scope-probe
Description: Library precedence fixture
Version: ${toString value}
Libs: -L$out/lib -lscopeprobe
Cflags: -I$out/include
EOF
    '';
  in { packages.%s = rec {
    first = probe 1;
    second = probe 2;
    propagated = pkgs.runCommand "scope-probe-propagated" {
      propagatedBuildInputs = [ first ];
    } ''
      mkdir -p $out/nix-support
      echo ${first} > $out/nix-support/propagated-build-inputs
    '';
  }; };
}`, nativeNixpkgs, system, system)
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "flake.nix"), []byte(flake), 0600))
	// Build only the test fixtures explicitly. Manager.Prepare must still use
	// its normal substitute-only policy; no local builds are enabled there.
	_, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{Command: []string{
		"nix", "--extra-experimental-features", "nix-command flakes", "build",
		"--max-jobs", "1", "--builders", "", "--no-link",
		fixture + "#first", fixture + "#second", fixture + "#propagated",
	}})
	require.NoError(t, err, string(stderr))
	first, second, propagated := "nix:"+fixture+"#first", "nix:"+fixture+"#second", "nix:"+fixture+"#propagated"
	m := Manager{Root: t.TempDir()}
	work := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(work, "probe.c"), []byte(`#include <scope-probe.h>
#include <stdio.h>
int main(void) { printf("%d %d\n", SCOPE_PROBE_VALUE, scope_probe()); }
`), 0600))
	for _, tc := range []struct {
		name   string
		scopes []Scope
		want   string
	}{
		{"inner_second", []Scope{{ID: "repository", Packages: []string{first}}, {ID: "op", Packages: []string{second}}}, "2"},
		{"inner_first", []Scope{{ID: "repository", Packages: []string{second}}, {ID: "op", Packages: []string{first}}}, "1"},
		{"redeclared_inner", []Scope{{ID: "repository", Packages: []string{first}}, {ID: "recipe", Packages: []string{second}}, {ID: "op", Packages: []string{first}}}, "1"},
		{"same_scope", []Scope{{ID: "op", Packages: []string{second, first}}}, "2"},
		{"propagated_outer", []Scope{{ID: "repository", Packages: []string{propagated}}, {ID: "op", Packages: []string{second}}}, "2"},
		{"propagated_inner", []Scope{{ID: "repository", Packages: []string{second}}, {ID: "op", Packages: []string{propagated}}}, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _, err := m.Prepare(ctx, tc.scopes)
			require.NoError(t, err)
			require.True(t, env.Ready())
			out, stderr, err := process.ExecuteProcess(env.Bind(ctx), process.RunRequest{WorkingDir: work, Shell: "sh", Run: `set -eu
pkg-config --modversion scope-probe
cc probe.c -o via-pc $(pkg-config --cflags --libs scope-probe)
./via-pc
cc probe.c -o direct -lscopeprobe
./direct
`})
			require.NoError(t, err, string(stderr))
			require.Equal(t, tc.want+"\n"+tc.want+" "+tc.want+"\n"+tc.want+" "+tc.want+"\n", string(out))
			// Reordered scopes must not reuse a differently ordered native cache.
			again, diag, err := m.Prepare(ctx, tc.scopes)
			require.NoError(t, err)
			require.Equal(t, env, again)
			require.Equal(t, "reused", diag.Native.Outcome)
		})
	}
}

func TestRealNativePythonCoexistence(t *testing.T) {
	if !nixtest.InContainer(t) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	// The two selectors from job DUAJz6xo2nyPk4jEINSbvs8v99B.
	repository := "nix:github:NixOS/nixpkgs/e4074f13905f5117d20ad71358596b41694863aa#python3"
	recipe := "nix:github:NixOS/nixpkgs/e7439b6b14ad3cc35d05608ebca9bce01a25f5f8#python3"
	m := Manager{Root: t.TempDir()}
	for _, tc := range []struct {
		name, outer, inner, version string
	}{
		{"recipe_override", repository, recipe, "3.14"},
		{"repository_override", recipe, repository, "3.13"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scopes := []Scope{{ID: "repository", Packages: []string{tc.outer}}, {ID: "op", Packages: []string{tc.inner}}}
			env, _, err := m.Prepare(ctx, scopes)
			require.NoError(t, err)
			out, stderr, err := process.ExecuteProcess(env.Bind(ctx), process.RunRequest{Shell: "sh", Run: `set -eu
python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])'
pkg-config --modversion python
`})
			require.NoError(t, err, string(stderr))
			require.Equal(t, []string{tc.version, tc.version}, strings.Fields(string(out)))
		})
	}
}
