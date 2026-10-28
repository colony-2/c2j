package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRepositoryPackagesParsing(t *testing.T) {
	for _, tc := range []struct{ name, text, failure string }{
		{"missing", "self: {repo: example/repo}", ""},
		{"empty", "packages: []", ""},
		{"null", "packages: null", ""},
		{"scalar", "packages: nix:nixpkgs#go", "list of strings"},
		{"number", "packages: [42]", "packages[0]: expected a string"},
		{"mapping", "packages: [{nix: go}]", "packages[0]: expected a string"},
		{"prefix", "packages: [npm:tool]", "packages[0]: unsupported package prefix"},
		{"template", "packages: ['uv:${{ inputs.tool }}']", "invalid package reference"},
		{"local_nix", "packages: ['nix:.#tool']", "portable"},
		{"local_pnpm", "packages: [pnpm:file:../tool]", "portable"},
		{"local_pnpm_named", "packages: [pnpm:@scope/tool@file:../tool]", "portable"},
		{"local_uv_git", "packages: ['uv:thing @ git+file:///tmp/tool']", "portable"},
		{"local_uv", "packages: ['uv:thing @ file:///tmp/tool']", "portable"},
		{"local_uv_compact", "packages: ['uv:thing@file:///tmp/tool']", "portable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseProjectConfig("/repo/.c2/config.yaml", []byte(tc.text))
			if tc.failure != "" {
				require.ErrorContains(t, err, tc.failure)
				return
			}
			require.NoError(t, err)
			require.Empty(t, cfg.Packages())
		})
	}
	cfg, err := parseProjectConfig("/repo/.c2/config.yaml", []byte("packages: [nix:github:NixOS/nixpkgs/abc#go, pnpm:@scope/tool@1.2.3, uv:ruff==1, uv:ruff==1]"))
	require.NoError(t, err)
	require.Equal(t, []string{"nix:github:NixOS/nixpkgs/abc#go", "pnpm:@scope/tool@1.2.3", "uv:ruff==1"}, cfg.Packages())
	copy := cfg.Packages()
	copy[0] = "changed"
	require.NotEqual(t, copy, cfg.Packages())
}

func TestWorkspacePackageSourceIsBoundedAndPure(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".c2"), 0700))
	path := filepath.Join(root, ".c2", "config.yaml")
	marker := filepath.Join(root, "command-ran")
	require.NoError(t, os.WriteFile(path, []byte("packages: [uv:tool==1]\nself: {repo: {command: 'touch "+marker+"'}}\nbase: deliberately-unrelated-invalid-base\n"), 0600))
	source, err := LoadWorkspacePackages(root)
	require.NoError(t, err)
	require.False(t, source.Missing)
	require.Equal(t, []string{"uv:tool==1"}, source.Packages)
	require.Contains(t, source.Digest, "sha256:")
	_, err = os.Stat(marker)
	require.True(t, os.IsNotExist(err))
	nested := filepath.Join(root, "nested")
	require.NoError(t, os.Mkdir(nested, 0700))
	empty, err := LoadWorkspacePackages(nested)
	require.NoError(t, err)
	require.True(t, empty.Missing)
	require.NoError(t, os.WriteFile(path, []byte("packages: [uv:tool==2]"), 0600))
	changed, err := LoadWorkspacePackages(root)
	require.NoError(t, err)
	require.NotEqual(t, source.Digest, changed.Digest)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(filepath.Join(root, "external"), []byte("packages: [uv:tool]"), 0600))
	require.NoError(t, os.Symlink("../external", path))
	_, err = LoadWorkspacePackages(root)
	require.ErrorContains(t, err, "symlinks")
	require.NoError(t, os.RemoveAll(filepath.Join(root, ".c2")))
	require.NoError(t, os.Symlink(nested, filepath.Join(root, ".c2")))
	_, err = LoadWorkspacePackages(root)
	require.ErrorContains(t, err, "symlinks")
}
