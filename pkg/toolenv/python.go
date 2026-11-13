package toolenv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/colony-2/c2j/internal/pathutil"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/ops/process"
)

const pythonFormat = "python-v1"

type pythonFile struct {
	Path     string `json:"path"`
	Target   string `json:"target"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

// PythonEnvironment is a retained installation at its final, non-relocatable path.
// Identity includes the interpreter and resolved dependency record. RequestKey is
// the local index used before resolution; cold caches may resolve differently.
type PythonEnvironment struct {
	Identity    string            `json:"identity"`
	RequestKey  string            `json:"request_key"`
	Path        string            `json:"path"`
	Executable  string            `json:"executable"`
	Version     string            `json:"version"`
	Interpreter json.RawMessage   `json:"interpreter"`
	Requested   *execution.Python `json:"requested"`
	Resolved    json.RawMessage   `json:"resolved"`
	Files       []pythonFile      `json:"files"`
}

type PythonDiagnostic struct {
	Scope       string            `json:"scope"`
	Outcome     string            `json:"outcome"`
	WallMS      int64             `json:"wall_ms"`
	Requested   *execution.Python `json:"requested"`
	Identity    string            `json:"identity,omitempty"`
	Executable  string            `json:"executable,omitempty"`
	Version     string            `json:"version,omitempty"`
	Resolved    json.RawMessage   `json:"resolved,omitempty"`
	CommandLogs []string          `json:"command_logs,omitempty"`
}

func pythonStamp(path string) (pythonFile, error) {
	canonical, err := pathutil.CanonicalExisting(path)
	if err != nil {
		return pythonFile{}, err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return pythonFile{}, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return pythonFile{}, err
	}
	return pythonFile{Path: absolute, Target: canonical, Size: info.Size(), Modified: info.ModTime().UnixNano()}, nil
}

func (p *PythonEnvironment) ready() bool {
	if p == nil || p.Identity == "" || len(p.Files) == 0 {
		return false
	}
	if info, err := os.Stat(p.Executable); err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		return false
	}
	for _, file := range p.Files {
		actual, err := pythonStamp(file.Path)
		if err != nil || actual != file {
			return false
		}
	}
	return true
}

// Resolve the nearest declaration before doing any work; overridden Python
// blocks are not installed. The lock covers lookup, preparation and publication.
func (m *Manager) preparePython(ctx context.Context, scopes []Scope, native *nativeEnvironment) (result *PythonEnvironment, diag *PythonDiagnostic, err error) {
	var requested *execution.Python
	var scope string
	for _, s := range scopes {
		if s.Python != nil {
			requested, scope = execution.ChoosePython(nil, s.Python), s.ID
		}
	}
	if requested == nil {
		return nil, nil, nil
	}
	sort.Strings(requested.Dependencies)
	requested.Dependencies = slices.Compact(requested.Dependencies)
	start := time.Now()
	diag = &PythonDiagnostic{Scope: scope, Outcome: "failed", Requested: requested}
	defer func() {
		diag.WallMS = time.Since(start).Milliseconds()
		if result != nil {
			diag.Identity, diag.Executable, diag.Version = result.Identity, result.Executable, result.Version
			diag.Resolved = result.Resolved
		}
	}()
	if err := requested.Validate(); err != nil {
		return nil, diag, err
	}
	if runtime.GOOS == "windows" {
		return nil, diag, fmt.Errorf("Python environment preparation requires a Unix worker")
	}
	if err := os.MkdirAll(m.Root, 0700); err != nil {
		return nil, diag, err
	}
	root, err := pathutil.CanonicalExisting(m.Root)
	if err != nil {
		return nil, diag, err
	}
	// Include interpreter discovery and resolver configuration, but never transient
	// offline/logging/link-mode settings. Values only enter a hash, not the manifest.
	policy := map[string]string{"PATH": os.Getenv("PATH")}
	// uv owns its configuration schema. Include worker uv settings by default,
	// excluding offline/cache/logging controls that must not defeat warm
	// reuse (and values explicitly overridden by this adapter).
	transient := map[string]bool{
		"UV_OFFLINE": true, "UV_CACHE_DIR": true, "UV_NO_CACHE": true,
		"UV_LINK_MODE": true, "UV_NO_PROGRESS": true, "UV_COLOR": true,
		"UV_VERBOSE": true, "UV_QUIET": true, "UV_CONCURRENT_DOWNLOADS": true,
		"UV_CONCURRENT_BUILDS": true, "UV_CONCURRENT_INSTALLS": true,
		"UV_PROJECT": true, "UV_WORKING_DIR": true, "UV_CONFIG_FILE": true,
		"UV_NO_CONFIG": true,
	}
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(name, "UV_") && !transient[name] {
			policy[name] = value
		}
	}

	nativeID := ""
	if native != nil {
		nativeID = native.Identity
	}
	requestKey := key([]any{pythonFormat, runtime.GOOS, runtime.GOARCH, root, requested, policy, nativeID})
	dir := filepath.Join(root, "python", requestKey)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, diag, err
	}
	unlock, err := lock(ctx, dir+".lock")
	if err != nil {
		return nil, diag, err
	}
	defer unlock()
	manifest := filepath.Join(dir, "ready.json")
	var cached PythonEnvironment
	if data, e := os.ReadFile(manifest); e == nil && json.Unmarshal(data, &cached) == nil && cached.RequestKey == requestKey && cached.ready() {
		diag.Outcome = "reused"
		return &cached, diag, nil
	}
	// A repair gets a new permanent path. Never delete or mutate an environment
	// that an earlier invocation could still be using.
	stage, err := os.MkdirTemp(dir, "environment-")
	if err != nil {
		return nil, diag, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()
	uv, err := exec.LookPath("uv")
	if err != nil {
		return nil, diag, fmt.Errorf("Python preparation requires uv: %w", err)
	}
	uv, err = pathutil.CanonicalExisting(uv)
	if err != nil {
		return nil, diag, err
	}
	run := func(executable string, args ...string) ([]byte, error) {
		capture := process.NewCapture(ctx)
		command := append([]string{executable}, args...)
		// Keep worker policy and credentials while removing ambient activation.
		env := map[string]string{}
		for _, entry := range gitprofile.Env(ctx, os.Environ()) {
			name, value, ok := strings.Cut(entry, "=")
			if ok {
				env[name] = value
			}
		}
		for _, name := range []string{"PYTHONPATH", "PYTHONHOME", "PYTHONUSERBASE", "VIRTUAL_ENV", "CONDA_PREFIX", "UV_PROJECT", "UV_WORKING_DIR", "UV_CONFIG_FILE"} {
			delete(env, name)
		}
		if native != nil {
			// uv cannot infer external compiler/header/library changes. Isolate
			// its built-wheel cache when the declared native environment changes.
			cache := env["UV_CACHE_DIR"]
			if cache == "" {
				cache = filepath.Join(root, "uv")
			}
			env["UV_CACHE_DIR"] = filepath.Join(cache, "c2j-native", native.Identity)
			for name, value := range native.Variables {
				if name == "PATH" {
					env[name] = value + string(os.PathListSeparator) + env[name]
				} else {
					env[name] = value
				}
			}
		}
		env["PYTHONNOUSERSITE"] = "1"
		_, _, runErr := process.ExecuteProcess(ctx, process.RunRequest{CleanEnv: true, WorkingDir: stage, Command: command, Env: env, Capture: capture})
		var log bytes.Buffer
		fmt.Fprintf(&log, "%s\n", strings.Join(command, " "))
		capture.WriteLog(&log)
		if executable == uv || runErr != nil {
			diag.CommandLogs = append(diag.CommandLogs, log.String())
		}
		if runErr != nil {
			return nil, fmt.Errorf("Python preparation %s: %w: %s", filepath.Base(executable), runErr, strings.TrimSpace(string(capture.Bytes(1))))
		}
		return capture.Bytes(0), nil
	}
	// JSON string quoting is also valid TOML basic-string quoting. Serialize each
	// dependency as one string: requirements-file options cannot become commands.
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	var metadata strings.Builder
	metadata.WriteString("[project]\nname = \"c2j-task-environment\"\nversion = \"0\"\n")
	if requested.RequiresPython != "" {
		fmt.Fprintf(&metadata, "requires-python = %s\n", quote(requested.RequiresPython))
	}
	metadata.WriteString("dependencies = [\n")
	for _, dep := range requested.Dependencies {
		fmt.Fprintf(&metadata, "  %s,\n", quote(dep))
	}
	metadata.WriteString("]\n")
	if err := os.WriteFile(filepath.Join(stage, "pyproject.toml"), []byte(metadata.String()), 0600); err != nil {
		return nil, diag, err
	}
	venv := filepath.Join(stage, "venv")
	args := []string{"venv", "--no-config", "--no-project"}
	if requested.RequiresPython != "" {
		args = append(args, "--python", requested.RequiresPython)
	}
	args = append(args, venv)
	if _, err := run(uv, args...); err != nil {
		return nil, diag, err
	}
	python := filepath.Join(venv, "bin", "python")
	// Resolution uses the selected interpreter, including markers and extras.
	if _, err := run(uv, "pip", "compile", "--no-config", "--python", python, "--output-file", "requirements.txt", "pyproject.toml"); err != nil {
		return nil, diag, err
	}
	linkMode := "hardlink"
	if runtime.GOOS == "darwin" {
		linkMode = "clone"
	}
	if _, err := run(uv, "pip", "sync", "--no-config", "--python", python, "--link-mode", linkMode, "requirements.txt"); err != nil {
		return nil, diag, err
	}
	resolved, err := run(uv, "pip", "list", "--no-config", "--python", python, "--format", "json")
	if err != nil {
		return nil, diag, err
	}
	if !json.Valid(resolved) {
		return nil, diag, fmt.Errorf("uv returned invalid resolved package metadata")
	}
	var canonicalResolved bytes.Buffer
	if err := json.Compact(&canonicalResolved, resolved); err != nil {
		return nil, diag, err
	}
	resolved = canonicalResolved.Bytes()
	probe, err := run(python, "-I", "-c", pythonProbe)
	if err != nil {
		return nil, diag, err
	}
	var canonicalProbe bytes.Buffer
	if err := json.Compact(&canonicalProbe, probe); err != nil {
		return nil, diag, err
	}
	probe = canonicalProbe.Bytes()
	var info struct {
		Base    string   `json:"base"`
		Version string   `json:"version"`
		Files   []string `json:"files"`
	}
	if err := json.Unmarshal(probe, &info); err != nil {
		return nil, diag, fmt.Errorf("Python interpreter metadata: %w", err)
	}
	result = &PythonEnvironment{RequestKey: requestKey, Path: venv, Executable: python, Version: info.Version, Interpreter: probe, Requested: requested, Resolved: resolved}
	paths := append(info.Files, python, info.Base, filepath.Join(venv, "pyvenv.cfg"), filepath.Join(stage, "requirements.txt"))
	sort.Strings(paths)
	for _, path := range paths {
		stamp, err := pythonStamp(path)
		if err != nil {
			return nil, diag, fmt.Errorf("Python readiness: %w", err)
		}
		result.Files = append(result.Files, stamp)
	}
	result.Identity = key([]any{pythonFormat, runtime.GOOS, runtime.GOARCH, requested, probe, resolved, result.Files})
	if !result.ready() {
		return nil, diag, fmt.Errorf("prepared Python environment is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return nil, diag, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, diag, err
	}
	if err := os.WriteFile(manifest+".tmp", data, 0600); err != nil {
		return nil, diag, err
	}
	if err := os.Rename(manifest+".tmp", manifest); err != nil {
		return nil, diag, err
	}
	published = true
	diag.Outcome = "prepared"
	return result, diag, nil
}

// Inspect distribution files without importing application modules. Missing
// installed files invalidate readiness; .pyc files are excluded because Python
// can create or replace them while using an otherwise immutable installation.
const pythonProbe = `import importlib.metadata, json, os, platform, sys, sysconfig
files = []
for distribution in importlib.metadata.distributions():
    for item in distribution.files or []:
        if str(item).endswith(('.pyc', '.pyo')):
            continue
        path = os.path.abspath(distribution.locate_file(item))
        files.append(path)
print(json.dumps(dict(base=sys._base_executable, version=platform.python_version(), implementation=sys.implementation.name, abi=sysconfig.get_config_var('SOABI'), platform=sysconfig.get_platform(), files=sorted(set(files)))))
`
