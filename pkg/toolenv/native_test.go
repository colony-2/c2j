package toolenv

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

func TestNativeLibraryAndCompilerConflicts(t *testing.T) {
	makeTool := func() Tool {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "lib/pkgconfig"), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(root, "lib/pkgconfig", "zlib.pc"), []byte("Version: test"), 0600))
		return Tool{Nix: &nixTool{Input: root}}
	}
	a, b := makeTool(), makeTool()
	_, _, err := nativeInputs([]Tool{a, b})
	require.ErrorContains(t, err, "conflicting Nix library module zlib.pc")
	inputs, _, err := nativeInputs([]Tool{a, a})
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	a.Nix.Compiler, b.Nix.Compiler = true, true
	_, _, err = nativeInputs([]Tool{a, b})
	require.ErrorContains(t, err, "conflicting Nix compilers")
}
