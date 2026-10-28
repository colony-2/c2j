package toolenv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/config"
	"github.com/colony-2/c2j/pkg/ops/process"
)

// Pin the activation ABI independently of user package selectors. Changing
// this revision or the capture/export contract changes the native cache key.
const nativeNixpkgs = "github:NixOS/nixpkgs/b6018f87da91d19d0ab4cf979885689b469cdd41"
const nativeVersion = "native-v1"

type RepositoryDiagnostic struct {
	config.PackageSource
	Scope      string `json:"scope"`
	Repository string `json:"repository,omitempty"`
	Snapshot   string `json:"snapshot,omitempty"`
}

type NativeDiagnostic struct {
	Identity string `json:"identity"`
	Outcome  string `json:"outcome"`
	WallMS   int64  `json:"wall_ms"`
}

type retainedRoot struct {
	Link   string `json:"link"`
	Target string `json:"target"`
}

type nixTool struct {
	Input    string         `json:"input"`
	Roots    []retainedRoot `json:"roots"`
	Activate bool           `json:"activate"`
	Compiler bool           `json:"compiler"`
}

type nativeEnvironment struct {
	Identity  string            `json:"identity"`
	Roots     []retainedRoot    `json:"roots"`
	Variables map[string]string `json:"variables"`
}

func rootsReady(roots []retainedRoot) bool {
	if len(roots) == 0 {
		return false
	}
	for _, root := range roots {
		path, err := filepath.EvalSymlinks(root.Link)
		if err != nil || path != root.Target {
			return false
		}
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

func (e nativeEnvironment) ready() bool {
	return len(e.Variables) > 0 && rootsReady(e.Roots)
}

// Select outputs while the derivation still has its metadata. Passing only a
// default output to mkShell would lose separate development outputs.
const nixToolMetadata = `p: {
  drv = p.drvPath;
  input = (p.dev or p).outPath;
  bin = (p.bin or p).outPath;
  main = p.meta.mainProgram or "";
  activate = (p ? dev) || (p ? lib);
  compiler = (p ? cc) && ((p.isGNU or false) || (p.isClang or false));
  outputs = builtins.listToAttrs (map (q: { name = q.outputName; value = q.outPath; })
    [ p (p.dev or p) (p.bin or p) (p.lib or p) ]);
}`

var nixOutputName = regexp.MustCompile(`^[a-zA-Z0-9_+-]+$`)

func prepareNixTool(ctx context.Context, dir, spec string, tool *Tool) error {
	raw, err := runNix(ctx, dir, "eval", "--json", "--apply", nixToolMetadata, "--", spec)
	if err != nil {
		return err
	}
	var meta struct {
		Drv, Input, Bin, Main string
		Activate, Compiler    bool
		Outputs               map[string]string
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return fmt.Errorf("Nix package metadata: %w", err)
	}
	if !nixStorePath.MatchString(meta.Drv) || !strings.HasSuffix(meta.Drv, ".drv") || !nixStorePath.MatchString(meta.Input) || !nixStorePath.MatchString(meta.Bin) || len(meta.Outputs) == 0 {
		return fmt.Errorf("invalid Nix package output metadata for %s", spec)
	}
	var names []string
	for name, path := range meta.Outputs {
		if !nixOutputName.MatchString(name) || !nixStorePath.MatchString(path) {
			return fmt.Errorf("invalid Nix output %q", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	// Explicit outputs avoid relying on profile output selection. The out-links
	// retain each output and its transitive runtime/propagated-input closure.
	if _, err := runNix(ctx, dir, "build", "--out-link", filepath.Join(dir, "result"), "--", meta.Drv+"^"+strings.Join(names, ",")); err != nil {
		return err
	}
	roots, err := outputRoots(dir, "result")
	if err != nil {
		return err
	}
	for _, path := range meta.Outputs {
		found := false
		for _, root := range roots {
			found = found || root.Target == path
		}
		if !found {
			return fmt.Errorf("Nix did not retain required output %s", path)
		}
	}
	tool.Bin = filepath.Join(meta.Bin, "bin")
	tool.Main = meta.Main
	tool.Nix = &nixTool{Input: meta.Input, Roots: roots, Activate: meta.Activate, Compiler: meta.Compiler}
	return nil
}

func outputRoots(dir, prefix string) ([]retainedRoot, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var roots []retainedRoot
	for _, entry := range entries {
		if entry.Name() != prefix && !strings.HasPrefix(entry.Name(), prefix+"-") {
			continue
		}
		link := filepath.Join(dir, entry.Name())
		target, err := filepath.EvalSymlinks(link)
		if err != nil {
			return nil, err
		}
		if !nixStorePath.MatchString(target) {
			return nil, fmt.Errorf("invalid retained Nix output %s", target)
		}
		roots = append(roots, retainedRoot{Link: link, Target: target})
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("Nix produced no retained outputs")
	}
	return roots, nil
}

func nativeLayout(input string) bool {
	for _, name := range []string{"include", "lib", "nix-support/setup-hook", "share/pkgconfig"} {
		if _, err := os.Stat(filepath.Join(input, name)); err == nil {
			return true
		}
	}
	return false
}

// A coherent library environment cannot use executable precedence to resolve
// duplicate pkg-config modules. Fail instead of depending on incidental order.
func nativeInputs(tools []Tool) (inputs []string, compiler bool, err error) {
	seen := map[string]bool{}
	var moduleDirs []string
	compilerPath := ""
	for _, tool := range tools {
		if tool.Nix == nil || seen[tool.Nix.Input] {
			continue
		}
		input := tool.Nix.Input
		seen[input] = true
		inputs = append(inputs, input)
		if tool.Nix.Compiler {
			if compilerPath != "" && compilerPath != input {
				return nil, false, fmt.Errorf("conflicting Nix compilers: %s and %s; use separate invocations", compilerPath, input)
			}
			compiler, compilerPath = true, input
		}
		for _, sub := range []string{"lib/pkgconfig", "share/pkgconfig"} {
			moduleDirs = append(moduleDirs, filepath.Join(input, sub))
		}
	}
	err = checkNativeModules(moduleDirs)
	return
}

func checkNativeModules(dirs []string) error {
	modules := map[string]string{}
	for _, dir := range dirs {
		entries, e := os.ReadDir(dir)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".pc") {
				continue
			}
			path, e := filepath.EvalSymlinks(filepath.Join(dir, entry.Name()))
			if e != nil {
				return e
			}
			if prior, ok := modules[entry.Name()]; ok && prior != path {
				return fmt.Errorf("conflicting Nix library module %s: %s and %s; use separate invocations", entry.Name(), prior, path)
			}
			modules[entry.Name()] = path
		}
	}
	return nil
}

func (m *Manager) prepareNative(ctx context.Context, tools []Tool) (env *nativeEnvironment, diag *NativeDiagnostic, err error) {
	needed := false
	for _, tool := range tools {
		needed = needed || (tool.Nix != nil && tool.Nix.Activate)
	}
	if !needed {
		return nil, nil, nil
	}
	start := time.Now()
	id := key([]any{nativeVersion, nativeNixpkgs, tools})
	diag = &NativeDiagnostic{Identity: id, Outcome: "failed"}
	defer func() { diag.WallMS = time.Since(start).Milliseconds() }()
	inputs, compiler, err := nativeInputs(tools)
	if err != nil {
		return nil, diag, err
	}
	dir := filepath.Join(m.Root, "native", id)
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return nil, diag, err
	}
	unlock, err := lock(ctx, dir+".lock")
	if err != nil {
		return nil, diag, err
	}
	defer unlock()
	manifest := filepath.Join(dir, "ready.json")
	var cached nativeEnvironment
	if b, e := os.ReadFile(manifest); e == nil && json.Unmarshal(b, &cached) == nil && cached.Identity == id && cached.ready() {
		diag.Outcome = "reused"
		return &cached, diag, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, diag, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, diag, err
	}
	system, err := NixSystem()
	if err != nil {
		return nil, diag, err
	}
	shell := "mkShell"
	if compiler {
		shell = "mkShellNoCC"
	}
	// JSON is a Nix string literal only after escaping interpolation as well.
	q := func(v string) string { b, _ := json.Marshal(v); return strings.ReplaceAll(string(b), "${", `\${`) }
	var paths []string
	for _, path := range inputs {
		paths = append(paths, "(builtins.storePath "+q(path)+")")
	}
	expr := fmt.Sprintf(`let pkgs = (builtins.getFlake %s).legacyPackages.%s;
in pkgs.%s { nativeBuildInputs = [ pkgs.pkg-config ]; buildInputs = [ %s ]; }`, q(nativeNixpkgs), system, shell, strings.Join(paths, " "))
	raw, err := runNix(ctx, dir, "derivation", "show", "--impure", "--expr", expr)
	if err != nil {
		return nil, diag, err
	}
	drv, err := decodeNativeDerivation(raw)
	if err != nil {
		return nil, diag, err
	}
	refs, err := drv.inputReferences()
	if err != nil {
		return nil, diag, err
	}
	args := append([]string{"build", "--out-link", filepath.Join(dir, "input"), "--"}, refs...)
	if _, err := runNix(ctx, dir, args...); err != nil {
		return nil, diag, err
	}
	roots, err := outputRoots(dir, "input")
	if err != nil {
		return nil, diag, err
	}
	variables, err := captureNative(ctx, dir, drv)
	if err != nil {
		return nil, diag, err
	}
	// Setup hooks may introduce propagated libraries beyond the direct inputs.
	if err := checkNativeModules(filepath.SplitList(variables["PKG_CONFIG_PATH"])); err != nil {
		return nil, diag, err
	}
	env = &nativeEnvironment{Identity: id, Roots: roots, Variables: variables}
	b, err := json.Marshal(env)
	if err != nil {
		return nil, diag, err
	}
	if err := os.WriteFile(manifest+".tmp", b, 0600); err != nil {
		return nil, diag, err
	}
	if err := os.Rename(manifest+".tmp", manifest); err != nil {
		return nil, diag, err
	}
	diag.Outcome = "prepared"
	return env, diag, nil
}

type nativeDerivation struct {
	Builder   string              `json:"builder"`
	Env       map[string]string   `json:"env"`
	InputDrvs map[string][]string `json:"inputDrvs"` // Nix legacy JSON
	Inputs    struct {
		Drvs map[string]struct {
			Outputs        []string                   `json:"outputs"`
			DynamicOutputs map[string]json.RawMessage `json:"dynamicOutputs"`
		} `json:"drvs"`
	} `json:"inputs"` // Nix version 4 JSON
}

func decodeNativeDerivation(raw []byte) (nativeDerivation, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return nativeDerivation{}, err
	}
	if version, ok := document["version"]; ok {
		if string(version) != "4" {
			return nativeDerivation{}, fmt.Errorf("unsupported Nix derivation JSON version %s", version)
		}
		data := document["derivations"]
		document = nil
		if err := json.Unmarshal(data, &document); err != nil {
			return nativeDerivation{}, err
		}
	}
	if len(document) != 1 {
		return nativeDerivation{}, fmt.Errorf("expected exactly one native environment derivation")
	}
	for _, data := range document {
		var drv nativeDerivation
		if err := json.Unmarshal(data, &drv); err != nil {
			return drv, err
		}
		if drv.Env["__structuredAttrs"] != "" || drv.Env["__json"] != "" {
			return drv, fmt.Errorf("structured Nix attributes are not supported for activation")
		}
		if !filepath.IsAbs(drv.Builder) || !nixStorePath.MatchString(drv.Env["stdenv"]) {
			return drv, fmt.Errorf("invalid native environment builder or stdenv")
		}
		return drv, nil
	}
	panic("unreachable")
}

func (d nativeDerivation) inputReferences() ([]string, error) {
	refs := map[string][]string{}
	for name, outputs := range d.InputDrvs {
		refs[name] = outputs
	}
	for name, input := range d.Inputs.Drvs {
		if len(input.DynamicOutputs) != 0 {
			return nil, fmt.Errorf("dynamic Nix derivation outputs are not supported for activation")
		}
		refs[name] = input.Outputs
	}
	var result []string
	for name, outputs := range refs {
		if !strings.HasPrefix(name, "/") {
			name = "/nix/store/" + name
		}
		if !nixStorePath.MatchString(name) || !strings.HasSuffix(name, ".drv") || len(outputs) == 0 {
			return nil, fmt.Errorf("invalid native environment input %q", name)
		}
		for _, output := range outputs {
			if !nixOutputName.MatchString(output) {
				return nil, fmt.Errorf("invalid Nix output %q", output)
			}
		}
		result = append(result, name+"^"+strings.Join(outputs, ","))
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("native environment has no inputs")
	}
	sort.Strings(result)
	return result, nil
}

// Export contract for native builds. Keep wrapper variables and discovery
// paths; exclude shell functions, arbitrary hooks, credentials and build state.
func nativeVariable(name string) bool {
	if strings.HasPrefix(name, "NIX_") {
		return name != "NIX_BUILD_TOP" && name != "NIX_BUILD_CORES"
	}
	switch name {
	case "PATH", "CC", "CXX", "CPP", "AR", "AS", "LD", "NM", "RANLIB", "STRIP", "OBJCOPY", "OBJDUMP", "READELF", "SIZE", "STRINGS", "PKG_CONFIG", "PKG_CONFIG_PATH", "PKG_CONFIG_LIBDIR", "CMAKE_PREFIX_PATH", "CMAKE_INCLUDE_PATH", "CMAKE_LIBRARY_PATH", "ACLOCAL_PATH":
		return true
	}
	return false
}

func captureNative(ctx context.Context, dir string, drv nativeDerivation) (map[string]string, error) {
	scratch, err := os.MkdirTemp(dir, "capture-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	env := drv.Env
	env["HOME"], env["TMPDIR"], env["NIX_BUILD_TOP"] = scratch, scratch, scratch
	env["out"], env["IN_NIX_SHELL"] = filepath.Join(scratch, "unused-output"), "impure"
	env["NIX_NO_SELF_RPATH"], env["NIX_ENFORCE_PURITY"] = "1", "0"
	stdout, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{
		WorkingDir: scratch, CleanEnv: true, Env: env,
		Command: []string{drv.Builder, "--noprofile", "--norc", "-c", `set -e; source "$stdenv/setup" >&2; env -0`},
	})
	if err != nil {
		return nil, fmt.Errorf("capture native environment: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	variables := map[string]string{}
	for _, entry := range strings.Split(string(stdout), "\x00") {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !nativeVariable(name) {
			continue
		}
		if strings.Contains(value, scratch) {
			return nil, fmt.Errorf("native activation variable %s contains a temporary capture path", name)
		}
		variables[name] = value
	}
	if variables["PATH"] == "" {
		return nil, fmt.Errorf("native activation produced no PATH")
	}
	return variables, nil
}
