package execution

import (
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"testing"
)

func TestPackageDeclarations(t *testing.T) {
	for _, ref := range []string{"nix:github:NixOS/nixpkgs/abc#jq", "uv:ruff==0.11.2", "pnpm:@scope/tool@1.2.3"} {
		_, _, err := ParsePackage(ref)
		require.NoError(t, err)
	}
	for _, ref := range []string{"npm:foo", "uv:", "uv:--help", "nix", "uv:foo\nbar", "uv:${{ inputs.tool }}"} {
		_, _, err := ParsePackage(ref)
		require.Error(t, err)
	}
	var req Requirements
	require.NoError(t, yaml.Unmarshal([]byte("packages: [uv:ruff==1, pnpm:typescript@2]"), &req))
	require.False(t, req.Empty())
	overlay := Overlay(req, Requirements{Packages: []string{"uv:ruff==2", "uv:ruff==1"}})
	require.Equal(t, []string{"uv:ruff==1", "pnpm:typescript@2", "uv:ruff==2"}, overlay.Packages)
	overlay.Packages[0] = "changed"
	require.Equal(t, "uv:ruff==1", req.Packages[0])
}
