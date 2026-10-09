// Package toolenv prepares retained, version-isolated CLI installations. It never
// installs tools from the timed operation path or changes the process environment.
package toolenv

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/ops/process"
)

type Scope struct {
	ID       string   `json:"id"`
	Packages []string `json:"packages"`
}

type Tool struct {
	Reference   string   `json:"reference"`
	Bin         string   `json:"bin"`
	Executables []string `json:"executables"`
	Main        string   `json:"main,omitempty"`
	Identity    string   `json:"identity"`
}

type ToolDiagnostic struct {
	Reference string `json:"reference"`
	Identity  string `json:"identity,omitempty"`
	Scope     string `json:"scope"`
	WallMS    int64  `json:"wall_ms"`
	Outcome   string `json:"outcome"`
}

type Diagnostics struct {
	TaskOrdinal *int64           `json:"task_ordinal,omitempty"`
	WallMS      int64            `json:"wall_ms"`
	Tools       []ToolDiagnostic `json:"tools,omitempty"`
	Error       string           `json:"error,omitempty"`
}

type Environment struct {
	Path  string `json:"path"`
	Tools []Tool `json:"tools"`
}

// Manager owns only a local cache. Providers can retain/mount Root at the same
// absolute path. Package managers and their runtimes must exist in the base.
type Manager struct{ Root string }

func Default() (*Manager, error) {
	root := os.Getenv("C2J_TOOL_CACHE_DIR")
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(cache, "c2j", "tools")
	}
	root, err := filepath.Abs(root)
	return &Manager{Root: root}, err
}

func key(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }

// Prepare resolves each declaration on first use, sharing completed entries
// across scopes/invocations. No manager runs on a warm hit.
func (m *Manager) Prepare(ctx context.Context, scopes []Scope) (env Environment, diag Diagnostics, err error) {
	start := time.Now()
	defer func() {
		diag.WallMS = time.Since(start).Milliseconds()
		if err != nil {
			diag.Error = err.Error()
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	if !filepath.IsAbs(m.Root) {
		return env, diag, fmt.Errorf("tool cache root must be absolute")
	}
	// One binding per executable, with the closest scope taking precedence.
	bindings := map[string]string{}
	all := map[string]Tool{}
	for _, scope := range scopes {
		local := map[string]string{}
		for _, ref := range scope.Packages {
			begin := time.Now()
			tool, hit, e := m.prepareTool(ctx, ref)
			outcome := "prepared"
			if hit {
				outcome = "reused"
			}
			if e != nil {
				outcome = "failed"
			}
			diag.Tools = append(diag.Tools, ToolDiagnostic{Reference: ref, Identity: tool.Identity, Scope: scope.ID, WallMS: time.Since(begin).Milliseconds(), Outcome: outcome})
			if e != nil {
				return env, diag, e
			}
			if _, ok := all[ref]; !ok {
				env.Tools = append(env.Tools, tool)
				all[ref] = tool
			}
			for _, name := range tool.Executables {
				p := filepath.Join(tool.Bin, name)
				if prior, ok := local[name]; ok && prior != p {
					local[name] = ""
				} else if !ok {
					local[name] = p
				}
			}
		}
		for name, p := range local {
			bindings[name] = p
		}
	}
	// Bindings have stable paths; publication is protected across worker processes.
	dir := filepath.Join(m.Root, "bindings", key(struct {
		Scopes []Scope
		Tools  []Tool
	}{scopes, env.Tools}))
	if err = os.MkdirAll(dir, 0700); err != nil {
		return
	}
	unlock, e := lock(ctx, dir+".lock")
	if e != nil {
		err = e
		return
	}
	defer unlock()
	for name, target := range bindings {
		// A same-scope collision must not silently fall through to a system version.
		body := "#!/bin/sh\necho 'ambiguous declared tool; use a qualified runner' >&2\nexit 127\n"
		if target != "" {
			body = "#!/bin/sh\nexec " + quote(target) + " \"$@\"\n"
		}
		if err = writeExecutable(filepath.Join(dir, name), body); err != nil {
			return
		}
	}
	if err = writeRunners(dir, env.Tools); err != nil {
		return
	}
	env.Path = dir
	return
}

func toolReady(t Tool) bool {
	if t.Bin == "" {
		return false
	}
	if stat, err := os.Stat(t.Bin); err != nil || !stat.IsDir() {
		return false
	}
	for _, name := range t.Executables {
		if stat, err := os.Stat(filepath.Join(t.Bin, name)); err != nil || stat.IsDir() || stat.Mode()&0111 == 0 {
			return false
		}
	}
	return true
}

// Ready is strictly local and read-only. False means return to setup, never
// install or resolve a package inside the task's execution budget.
func (e Environment) Ready() bool {
	if e.Path == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(e.Path, "uvx")); err != nil {
		return false
	}
	for _, t := range e.Tools {
		if !toolReady(t) {
			return false
		}
	}
	return true
}

func (m *Manager) prepareTool(ctx context.Context, ref string) (Tool, bool, error) {
	if err := ctx.Err(); err != nil {
		return Tool{}, false, err
	}
	manager, spec, err := execution.ParsePackage(ref)
	if err != nil {
		return Tool{}, false, err
	}
	id := key([]string{"v1", runtime.GOOS, runtime.GOARCH, ref})
	dir := filepath.Join(m.Root, "packages", id)
	if err = os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return Tool{}, false, err
	}
	unlock, err := lock(ctx, dir+".lock")
	if err != nil {
		return Tool{}, false, err
	}
	defer unlock()
	manifest := filepath.Join(dir, "ready.json")
	var t Tool
	if b, e := os.ReadFile(manifest); e == nil && json.Unmarshal(b, &t) == nil && t.Reference == ref && toolReady(t) {
		return t, true, nil
	}
	// Incomplete installations cannot be published or reused. Install at the final
	// path: Python virtual environments and pnpm launchers are not relocatable.
	if err = os.RemoveAll(dir); err != nil {
		return Tool{}, false, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return Tool{}, false, err
	}
	t = Tool{Reference: ref, Identity: id}
	run := func(command []string, env map[string]string) ([]byte, error) {
		out, stderr, e := process.ExecuteProcess(ctx, process.RunRequest{WorkingDir: dir, Command: command, Env: env})
		if e != nil {
			s := strings.TrimSpace(string(stderr))
			if len(s) > 4096 {
				s = s[len(s)-4096:]
			}
			return nil, fmt.Errorf("prepare %s: %w: %s", ref, e, s)
		}
		return out, nil
	}
	switch manager {
	case "uv":
		t.Bin = filepath.Join(dir, "bin")
		_, err = run([]string{"uv", "tool", "install", "--", spec}, map[string]string{"UV_TOOL_DIR": filepath.Join(dir, "tools"), "UV_TOOL_BIN_DIR": t.Bin})
	case "pnpm":
		t.Bin = filepath.Join(dir, "node_modules", ".bin")
		err = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"private":true}`), 0600)
		if err == nil {
			_, err = run([]string{"pnpm", "add", "--save-exact", "--", spec}, map[string]string{"CI": "true"})
		}
	case "nix":
		t.Bin = filepath.Join(dir, "result", "bin")
		_, err = run(nixSetupCommand("build", "--out-link", filepath.Join(dir, "result"), "--", spec), nil)
	}
	if err != nil {
		return Tool{}, false, err
	}
	entries, err := os.ReadDir(t.Bin)
	if err != nil {
		return Tool{}, false, fmt.Errorf("%s exports no tool directory: %w", ref, err)
	}
	for _, entry := range entries {
		if stat, e := os.Stat(filepath.Join(t.Bin, entry.Name())); e == nil && !stat.IsDir() && stat.Mode()&0111 != 0 {
			t.Executables = append(t.Executables, entry.Name())
		}
	}
	if len(t.Executables) == 0 {
		return Tool{}, false, fmt.Errorf("%s exports no executables", ref)
	}
	if manager == "nix" {
		if len(t.Executables) == 1 {
			t.Main = t.Executables[0]
		} else {
			// meta.mainProgram is the native nix run selection for multi-binary packages.
			out, e := run(nixSetupCommand("eval", "--raw", spec+".meta.mainProgram"), nil)
			if e == nil {
				t.Main = strings.TrimSpace(string(out))
			}
			if t.Main == "" {
				_, attr, _ := strings.Cut(spec, "#")
				parts := strings.Split(attr, ".")
				name := parts[len(parts)-1]
				for _, exe := range t.Executables {
					if exe == name {
						t.Main = name
					}
				}
			}
		}
	}
	if t.Main != "" {
		found := false
		for _, name := range t.Executables {
			if name == t.Main {
				found = true
			}
		}
		if !found {
			t.Main = ""
		}
	}
	b, err := json.Marshal(t)
	if err != nil {
		return Tool{}, false, err
	}
	if err = os.WriteFile(manifest+".tmp", b, 0600); err == nil {
		err = os.Rename(manifest+".tmp", manifest)
	}
	return t, false, err
}

// Setup may reuse store outputs or download substitutes, but must never build
// locally or remotely. Apply the same policy to evaluation, which can itself
// request derivations through import-from-derivation.
func nixSetupCommand(args ...string) []string {
	return append([]string{
		"nix", "--extra-experimental-features", "nix-command flakes",
		"--max-jobs", "0", "--builders", "",
	}, args...)
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func writeExecutable(path, body string) error {
	if b, err := os.ReadFile(path); err == nil && string(b) == body {
		return nil
	}
	if err := os.WriteFile(path+".tmp", []byte(body), 0700); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// These exact native invocation forms dispatch retained executables. They never
// fall back to downloading an undeclared version during timed execution.
func writeRunners(dir string, tools []Tool) error {
	scripts := map[string]string{
		"uvx":  "#!/bin/sh\n[ \"$1\" = --from ] || { echo 'use uvx --from PACKAGE EXECUTABLE' >&2; exit 127; }\nspec=$2; shift 2\ncase \"$spec\" in\n",
		"pnpm": "#!/bin/sh\ncase \"$1\" in --package=*) spec=${1#--package=}; shift;; --package) spec=$2; shift 2;; *) echo 'use pnpm --package=PACKAGE dlx EXECUTABLE' >&2; exit 127;; esac\n[ \"$1\" = dlx ] || exit 127\nshift\ncase \"$spec\" in\n",
		"nix":  "#!/bin/sh\n[ \"$1\" = run ] || { echo 'use nix run INSTALLABLE -- ARGS' >&2; exit 127; }\nspec=$2; shift 2\n[ \"$1\" != -- ] || shift\ncase \"$spec\" in\n",
	}
	for _, t := range tools {
		manager, spec, _ := execution.ParsePackage(t.Reference)
		runner := manager
		if manager == "uv" {
			runner = "uvx"
		}
		branch := quote(spec) + ")\n"
		if manager == "nix" {
			if t.Main == "" {
				branch += "echo 'package has no unambiguous main program; use its executable name' >&2; exit 127\n"
			} else {
				branch += "exec " + quote(filepath.Join(t.Bin, t.Main)) + " \"$@\"\n"
			}
		} else {
			branch += "case \"$1\" in\n"
			for _, name := range t.Executables {
				branch += quote(name) + ") shift; exec " + quote(filepath.Join(t.Bin, name)) + " \"$@\";;\n"
			}
			branch += "*) echo 'executable not declared by package' >&2; exit 127;;\nesac\n"
		}
		scripts[runner] += branch + ";;\n"
	}
	for name, body := range scripts {
		if native, e := exec.LookPath(name); e == nil && name == "pnpm" {
			body = strings.Replace(body, "*) echo 'use pnpm --package=PACKAGE dlx EXECUTABLE' >&2; exit 127;;", "*) exec "+quote(native)+" \"$@\";;", 1)
		}
		if native, e := exec.LookPath(name); e == nil && name == "nix" {
			body = strings.Replace(body, "echo 'use nix run INSTALLABLE -- ARGS' >&2; exit 127;", "exec "+quote(native)+" \"$@\";", 1)
		}
		body += "*) echo 'tool version not declared' >&2; exit 127;;\nesac\n"
		if err := writeExecutable(filepath.Join(dir, name), body); err != nil {
			return err
		}
	}
	return nil
}
