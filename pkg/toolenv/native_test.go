package toolenv

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/pathutil"
	"github.com/colony-2/c2j/pkg/shellcmd"
	"github.com/stretchr/testify/require"
)

// The recorder deliberately fails realization: argument and validation tests
// must not depend on writable store paths or claim to test Nix substitution.
func TestNixEvaluatedOutputRequest(t *testing.T) {
	const a = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-a"
	const b = "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-b"
	const c = "/nix/store/cccccccccccccccccccccccccccccccc-c"
	for _, tc := range []struct {
		name      string
		change    func(map[string]any)
		wantBuild bool
	}{
		{"sorted_unique_paths", func(m map[string]any) {}, true},
		{"invalid_derivation", func(m map[string]any) { m["drv"] = a }, false},
		{"invalid_input", func(m map[string]any) { m["input"] = "/tmp/input" }, false},
		{"unselected_input", func(m map[string]any) { m["input"] = c }, false},
		{"unselected_bin", func(m map[string]any) { m["bin"] = c }, false},
		{"invalid_bin", func(m map[string]any) { m["bin"] = b + "/bin" }, false},
		{"empty_outputs", func(m map[string]any) { m["outputs"] = map[string]string{} }, false},
		{"invalid_name", func(m map[string]any) { m["outputs"] = map[string]string{"out^dev": a} }, false},
		{"invalid_path", func(m map[string]any) { m["outputs"] = map[string]string{"out": "/tmp/out"} }, false},
		{"wrong_type", func(m map[string]any) { m["outputs"] = []string{a} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := map[string]any{"drv": a + ".drv", "input": a, "bin": b,
				"outputs": map[string]string{"z": a, "out": b, "dev": a, "lib": b}}
			tc.change(meta)
			raw, err := json.Marshal(meta)
			require.NoError(t, err)
			log := recordNix(t, string(raw), "exit 19")
			m := Manager{Root: t.TempDir()}
			_, diag, err := m.Prepare(t.Context(), []Scope{{ID: "op", Packages: []string{"nix:test#tool"}}})
			require.Error(t, err)
			require.Equal(t, "failed", diag.Tools[0].Outcome)
			commands, err := os.ReadFile(log)
			require.NoError(t, err)
			require.Equal(t, tc.wantBuild, strings.Contains(string(commands), "\nbuild\n"))
			if tc.wantBuild {
				parts := strings.Split(string(commands), "\nbuild\n")
				args := strings.Split(strings.TrimSuffix(parts[1], "\n"), "\n")
				require.Len(t, args, 5)
				require.Equal(t, "--out-link", args[0])
				require.Equal(t, []string{"--", a, b}, args[2:])
			}
			manifests, err := filepath.Glob(filepath.Join(m.Root, "packages", "*", "ready.json"))
			require.NoError(t, err)
			require.Empty(t, manifests)
		})
	}
}

func recordNix(t *testing.T, metadata, build string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "commands")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" >> ` + shellcmd.QuotePOSIX(log) + `
[ "$1" = --extra-experimental-features ]; shift 2
[ "$1" = --max-jobs ] && [ "$2" = 0 ]; shift 2
[ "$1" = --builders ] && [ -z "$2" ]; shift 2
[ "$1" = --option ] && [ "$2" = allow-import-from-derivation ] && [ "$3" = false ]; shift 3
case "$1" in
eval) ` + shellcmd.LiteralLine(metadata) + `;;
build) ` + build + `;;
*) exit 98;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "nix"), []byte(script), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestNixSuccessfulRealizationRequiresRoots(t *testing.T) {
	const path = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-output"
	for _, build := range []string{"exit 0", "ln -s /tmp \"$3\"", "ln -s " + path + " \"$3\"", "ln -s /tmp \"$3\"; exit 1"} {
		t.Run(build, func(t *testing.T) {
			recordNix(t, `{"drv":"`+path+`.drv","input":"`+path+`","bin":"`+path+`","outputs":{"out":"`+path+`"}}`, build)
			m := Manager{Root: t.TempDir()}
			env, diag, err := m.Prepare(t.Context(), []Scope{{ID: "op", Packages: []string{"nix:test#tool"}}})
			require.Error(t, err)
			require.False(t, env.Ready())
			require.Equal(t, "failed", diag.Tools[0].Outcome)
			manifests, err := filepath.Glob(filepath.Join(m.Root, "packages", "*", "ready.json"))
			require.NoError(t, err)
			require.Empty(t, manifests)
		})
	}
}

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

func TestNativeDerivationLiteralFixtures(t *testing.T) {
	for _, tc := range []struct {
		name string
		refs []string
	}{
		{"ubuntu-nix-2.18.1", []string{
			"/nix/store/3gs3nhybxpgf6wj1ik794b9ypysvh1gb-pkg-config-wrapper-0.29.2.drv^out",
			"/nix/store/iih3d02zns7xn54kv6w3vv2j9zfh9qm3-stdenv-linux.drv^out",
			"/nix/store/m5z7qjlfzjqrya90krpxvb3v7chz6gpk-gcc-wrapper-16.2.0.drv^out",
			"/nix/store/skb0fi9ii07jk7xnbpi283s1fnawlk8l-bash-5.3p3.drv^out",
		}},
		{"legacy-array", []string{"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-input.drv^out", "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-input.drv^dev,out"}},
		{"version-4", []string{"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-input.drv^out", "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-input.drv^dev,out"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata/native", tc.name+".json"))
			require.NoError(t, err)
			if tc.name == "ubuntu-nix-2.18.1" {
				require.Equal(t, "f4e710eceebd3e8f4dcdc6a6553bb7b40b6c5e3ab93bdc55c5807da077a811ae", fmt.Sprintf("%x", sha256.Sum256(data)))
			}
			drv, err := decodeNativeDerivation(data)
			require.NoError(t, err)
			refs, err := drv.inputReferences()
			require.NoError(t, err)
			require.Equal(t, tc.refs, refs)
		})
	}
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

// Keep these JSON cases independent of the production representation.
const nativeTestHeader = `{"shell":{"builder":"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bash/bin/bash","env":{"stdenv":"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-stdenv"},`
const nativeTestInput = `/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-input.drv`

func TestNativeDerivationInputValidation(t *testing.T) {
	for _, location := range []string{"inputDrvs", "inputs"} {
		for _, tc := range []struct{ name, entry, error string }{
			{"array", `["dev","out"]`, ""},
			{"object", `{"outputs":["out"],"dynamicOutputs":{}}`, ""},
			{"omitted_dynamic", `{"outputs":["out"],"futureField":true}`, ""},
			{"null_dynamic", `{"outputs":["out"],"dynamicOutputs":null}`, ""},
			{"dynamic", `{"outputs":["out"],"dynamicOutputs":{"out":{}}}`, "dynamic"},
			{"null", `null`, "input"},
			{"string", `"out"`, "input"},
			{"number", `7`, "input"},
			{"boolean", `true`, "input"},
			{"bad_outputs", `{"outputs":"out"}`, "unmarshal"},
			{"bad_array", `["out",7]`, "unmarshal"},
			{"bad_dynamic_array", `{"outputs":["out"],"dynamicOutputs":[]}`, "unmarshal"},
			{"bad_dynamic_scalar", `{"outputs":["out"],"dynamicOutputs":true}`, "unmarshal"},
			{"missing_outputs", `{}`, "invalid native"},
			{"empty_outputs", `[]`, "invalid native"},
			{"null_outputs", `{"outputs":null}`, "invalid native"},
			{"empty_object_outputs", `{"outputs":[]}`, "invalid native"},
			{"null_output", `[null]`, "invalid Nix output"},
			{"empty_output", `[""]`, "invalid Nix output"},
			{"bad_output", `["out^dev"]`, "invalid Nix output"},
		} {
			t.Run(location+"/"+tc.name, func(t *testing.T) {
				entries := `{"` + nativeTestInput + `":` + tc.entry + `}`
				if location == "inputs" {
					entries = `{"drvs":` + entries + `}`
				}
				drv, err := decodeNativeDerivation([]byte(nativeTestHeader + `"` + location + `":` + entries + `}}`))
				var refs []string
				if err == nil {
					refs, err = drv.inputReferences()
				}
				if tc.error != "" {
					require.ErrorContains(t, err, tc.error)
					return
				}
				require.NoError(t, err)
				if tc.name == "array" {
					require.Equal(t, []string{nativeTestInput + "^dev,out"}, refs)
				} else {
					require.Equal(t, []string{nativeTestInput + "^out"}, refs)
				}
			})
		}
	}
	for _, path := range []string{"/tmp/input.drv", "/nix/store/bad.drv", "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-input", "../input.drv"} {
		t.Run(path, func(t *testing.T) {
			drv, err := decodeNativeDerivation([]byte(nativeTestHeader + `"inputDrvs":{"` + path + `":["out"]}}}`))
			require.NoError(t, err)
			_, err = drv.inputReferences()
			require.ErrorContains(t, err, "invalid native")
		})
	}
}

func TestNativeDerivationMixedAndShadowedEntries(t *testing.T) {
	for _, tc := range []struct{ name, legacy, modern, error string }{
		{"precedence", `["dev"]`, `{"outputs":["out"]}`, ""},
		{"legacy_dynamic", `{"outputs":["dev"],"dynamicOutputs":{"out":{}}}`, `{"outputs":["out"]}`, "dynamic"},
		{"modern_dynamic", `["dev"]`, `{"outputs":["out"],"dynamicOutputs":{"out":{}}}`, "dynamic"},
		{"shadowed_empty", `[]`, `{"outputs":["out"]}`, "invalid native"},
		{"shadowed_bad_output", `["bad^output"]`, `{"outputs":["out"]}`, "invalid Nix output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := nativeTestHeader + `"inputDrvs":{"` + nativeTestInput + `":` + tc.legacy + `,"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-other.drv":{"outputs":["dev","out"]}},"inputs":{"drvs":{"` + nativeTestInput + `":` + tc.modern + `}}}}`
			drv, err := decodeNativeDerivation([]byte(data))
			require.NoError(t, err)
			for i := 0; i < 10; i++ {
				refs, err := drv.inputReferences()
				if tc.error != "" {
					require.ErrorContains(t, err, tc.error)
					continue
				}
				require.NoError(t, err)
				require.Equal(t, []string{nativeTestInput + "^out", "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-other.drv^dev,out"}, refs)
			}
		})
	}
}

// Exact-key precedence must not introduce deduplication of relative/absolute aliases.
func TestNativeDerivationKeepsDistinctInputKeys(t *testing.T) {
	data := nativeTestHeader + `"inputDrvs":{"` + nativeTestInput + `":["out"],"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-input.drv":{"outputs":["out"]}}}}`
	drv, err := decodeNativeDerivation([]byte(data))
	require.NoError(t, err)
	refs, err := drv.inputReferences()
	require.NoError(t, err)
	require.Equal(t, []string{nativeTestInput + "^out", nativeTestInput + "^out"}, refs)
}

func TestNativeDerivationEnvelopeAndActivationValidation(t *testing.T) {
	for _, tc := range []struct{ name, document, error string }{
		{"version", `{"version":999}`, "unsupported"},
		{"string_version", `{"version":"4","derivations":{}}`, "unsupported"},
		{"no_derivations", `{"version":4}`, "JSON"},
		{"bad_derivations", `{"version":4,"derivations":[]}`, "unmarshal"},
		{"empty", `{}`, "exactly one"},
		{"null", `null`, "exactly one"},
		{"version4_empty", `{"version":4,"derivations":{}}`, "exactly one"},
		{"version4_null", `{"version":4,"derivations":null}`, "exactly one"},
		{"multiple", `{"a":{},"b":{}}`, "exactly one"},
		{"version4_multiple", `{"version":4,"derivations":{"a":{},"b":{}}}`, "exactly one"},
		{"array_document", `[]`, "unmarshal"},
		{"truncated", `{`, "JSON"},
		{"null_drv", `{"a":null}`, "builder or stdenv"},
		{"relative_builder", `{"a":{"builder":"bash","env":{"stdenv":"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-stdenv"}}}`, "builder or stdenv"},
		{"invalid_stdenv", `{"a":{"builder":"/bin/bash","env":{"stdenv":"/tmp/stdenv"}}}`, "builder or stdenv"},
		{"structured", `{"a":{"env":{"__structuredAttrs":"1"}}}`, "structured"},
		{"structured_json", `{"a":{"env":{"__json":"{}"}}}`, "structured"},
		{"no_inputs", nativeTestHeader + `"inputDrvs":{}}}`, "no inputs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drv, err := decodeNativeDerivation([]byte(tc.document))
			if err == nil {
				_, err = drv.inputReferences()
			}
			require.ErrorContains(t, err, tc.error)
		})
	}
}

func TestNativePreparationRejectsInputsBeforeRealization(t *testing.T) {
	for _, tc := range []struct{ name, entry, error string }{
		{"malformed", `null`, "input"},
		{"empty", `[]`, "invalid native"},
		{"dynamic", `{"outputs":["out"],"dynamicOutputs":{"out":{}}}`, "dynamic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			log := filepath.Join(t.TempDir(), "operations")
			data := nativeTestHeader + `"inputDrvs":{"` + nativeTestInput + `":` + tc.entry + `}}}`
			script := "#!/bin/sh\nset -eu\n"
			script += `for arg do
 case "$arg" in
 derivation) ` + shellcmd.LiteralLine("derivation") + ` >> ` + shellcmd.QuotePOSIX(log) + "\n" + shellcmd.LiteralLine(data) + `; exit 0;;
 build) ` + shellcmd.LiteralLine("UNEXPECTED_REALIZATION") + ` >> ` + shellcmd.QuotePOSIX(log) + `; exit 99;;
 esac
done
exit 98
`
			require.NoError(t, os.WriteFile(filepath.Join(bin, "nix"), []byte(script), 0700))
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			m := Manager{Root: t.TempDir()}
			env, diag, err := m.prepareNative(context.Background(), []Tool{{Nix: &nixTool{Input: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-input", Activate: true}}})
			require.ErrorContains(t, err, tc.error)
			require.Nil(t, env)
			require.NotEmpty(t, diag.Identity)
			require.Equal(t, "failed", diag.Outcome)
			operations, err := os.ReadFile(log)
			require.NoError(t, err)
			require.Equal(t, "derivation\n", string(operations))
			files, err := filepath.Glob(filepath.Join(m.Root, "native", "*", "*"))
			require.NoError(t, err)
			require.Empty(t, files, "no retained input roots, capture directory or ready manifest")
		})
	}
}
