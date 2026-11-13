package toolenv

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/pathutil"
	"github.com/stretchr/testify/require"
)

func TestNativeCaptureRejectsFailedHooksAndTemporaryPaths(t *testing.T) {
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	for _, tc := range []struct{ name, hook, error string }{
		{"failed_hook", "return 7", "capture native environment"},
		{"temporary_path", `export NIX_LDFLAGS="-L$out/lib"`, "temporary capture path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "setup"), []byte(tc.hook), 0600))
			_, err := captureNative(context.Background(), root, nativeDerivation{Builder: bash, Env: map[string]string{"stdenv": root, "PATH": os.Getenv("PATH")}})
			require.ErrorContains(t, err, tc.error)
		})
	}
}

func TestNativeDerivationFormatsAndUnsupportedInputs(t *testing.T) {
	store := "/nix/store/" + strings.Repeat("a", 32)
	for _, version4 := range []bool{false, true} {
		drv := nativeDerivation{Builder: store + "-bash/bin/bash", Env: map[string]string{"stdenv": store + "-stdenv"}, InputDrvs: map[string][]string{store + "-input.drv": {"dev", "out"}}}
		document := any(map[string]any{store + "-shell.drv": drv})
		if version4 {
			document = map[string]any{"version": 4, "derivations": document}
		}
		data, err := json.Marshal(document)
		require.NoError(t, err)
		parsed, err := decodeNativeDerivation(data)
		require.NoError(t, err)
		refs, err := parsed.inputReferences()
		require.NoError(t, err)
		require.Equal(t, []string{store + "-input.drv^dev,out"}, refs)
	}
	_, err := decodeNativeDerivation([]byte(`{"version":999}`))
	require.ErrorContains(t, err, "unsupported")
	_, err = (nativeDerivation{InputDrvs: map[string][]string{"/tmp/not-a-derivation": {"out"}}}).inputReferences()
	require.ErrorContains(t, err, "invalid")
	var dynamic nativeDerivation
	require.NoError(t, json.Unmarshal([]byte(`{"inputs":{"drvs":{"some.drv":{"outputs":["out"],"dynamicOutputs":{"out":{}}}}}}`), &dynamic))
	_, err = dynamic.inputReferences()
	require.ErrorContains(t, err, "dynamic")
}

func TestNativeLibraryCoexistenceAndCompilerConflicts(t *testing.T) {
	makeTool := func() Tool {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "lib/pkgconfig"), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(root, "lib/pkgconfig", "zlib.pc"), []byte("Version: test"), 0600))
		return Tool{Nix: &nixTool{Input: root}}
	}
	a, b := makeTool(), makeTool()
	inputs, _, err := nativeInputs([]Tool{a, b})
	require.NoError(t, err)
	require.Equal(t, []string{a.Nix.Input, b.Nix.Input}, inputs)
	inputs, _, err = nativeInputs([]Tool{a, a})
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	a.Nix.Compiler, b.Nix.Compiler = true, true
	_, _, err = nativeInputs([]Tool{a, b})
	require.ErrorContains(t, err, "conflicting Nix compilers")
}

func TestNativeScopeOrderKeepsClosestStoreInput(t *testing.T) {
	prepared := map[string]Tool{}
	for _, ref := range []string{"outer", "middle", "first", "second"} {
		prepared[ref] = Tool{Reference: ref, Nix: &nixTool{Input: "/nix/store/" + ref}}
	}
	prepared["alias"] = Tool{Reference: "alias", Nix: &nixTool{Input: prepared["outer"].Nix.Input}}
	prepared["uv:helper"] = Tool{Reference: "uv:helper"}
	scopes := []Scope{
		{ID: "repo", Packages: []string{"outer", "second"}},
		{ID: "recipe", Packages: []string{"middle"}},
		{ID: "op", Packages: []string{"first", "alias", "second", "first", "uv:helper"}},
	}
	tools := nativeToolsByScope(scopes, prepared)
	var refs []string
	for _, tool := range tools {
		refs = append(refs, tool.Reference)
	}
	require.Equal(t, []string{"first", "alias", "second", "middle"}, refs)
}

// Readiness must compare the retained identity strictly, even when both objects
// still exist. These checks require no Nix installation.
func TestRetainedRootReadiness(t *testing.T) {
	for _, change := range []string{"valid", "missing link", "missing target", "retargeted"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "target")
			require.NoError(t, os.Mkdir(target, 0700))
			identity, err := pathutil.CanonicalExisting(target)
			require.NoError(t, err)
			link := filepath.Join(dir, "root")
			require.NoError(t, os.Symlink(target, link))
			tool := Tool{Nix: &nixTool{Input: identity, Roots: []retainedRoot{{Link: link, Target: identity}}}}
			require.True(t, toolReady(tool))
			switch change {
			case "missing link":
				require.NoError(t, os.Remove(link))
			case "missing target":
				require.NoError(t, os.Remove(target))
			case "retargeted":
				other := filepath.Join(dir, "other")
				require.NoError(t, os.Mkdir(other, 0700))
				require.NoError(t, os.Remove(link))
				require.NoError(t, os.Symlink(other, link))
			}
			require.Equal(t, change == "valid", toolReady(tool))
		})
	}
}
