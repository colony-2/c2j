package compiler

import "testing"

// This executes a real Nix-built probe, including installed/described manifest
// parity, missing-cache failure and replay after removing the package and source.
// nixtest reports missing Docker as a skip, not successful packaged acceptance.
func TestRealNixArtifactFiles(t *testing.T) {
	testRealNixExtension(t, true)
}

// Parsing this fixture requires no package tools; it cannot establish package execution.
func TestArtifactFilePackagedRecipeSyntax(t *testing.T) {
	for _, direct := range []bool{false, true} {
		realNixExtensionRecipe(t, "nix:path:/tmp/definition#echo", direct, "/tmp/prefix")
	}
}
