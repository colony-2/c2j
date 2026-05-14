package extensions

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/colony-2/shai/pkg/shai"
	yaml "gopkg.in/yaml.v3"
)

const (
	SandboxTypeNone = "none"
	SandboxTypeShai = "shai"
)

type SandboxInput struct {
	Type         string         `json:"type,omitempty" yaml:"type,omitempty"`
	InlineConfig map[string]any `json:"inline_config,omitempty" yaml:"inline_config,omitempty"`
}

type RunRequest struct {
	WorkspaceRoot string
	WorkingDir    string
	Shell         string
	Run           string
	Command       []string
	Env           map[string]string
	Stdin         []byte
	Sandbox       *SandboxInput
}

func ParseSandboxInput(raw interface{}) (*SandboxInput, error) {
	if raw == nil {
		return nil, nil
	}
	buf, err := yaml.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode sandbox input: %w", err)
	}
	var sandbox SandboxInput
	if err := yaml.Unmarshal(buf, &sandbox); err != nil {
		return nil, fmt.Errorf("decode sandbox input: %w", err)
	}
	sandbox.Type = strings.TrimSpace(sandbox.Type)
	switch sandbox.Type {
	case "", SandboxTypeNone:
		return &sandbox, nil
	case SandboxTypeShai:
		return &sandbox, nil
	default:
		return nil, fmt.Errorf("unsupported sandbox type %q", sandbox.Type)
	}
}

func ExecuteProcess(ctx context.Context, req RunRequest) ([]byte, []byte, error) {
	workspaceRoot := strings.TrimSpace(req.WorkspaceRoot)
	workingDir := strings.TrimSpace(req.WorkingDir)
	if workspaceRoot == "" {
		workspaceRoot = workingDir
	}
	if workingDir == "" {
		workingDir = workspaceRoot
	}
	if workspaceRoot == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, nil, err
		}
		workspaceRoot = wd
		workingDir = wd
	}

	sandboxType := SandboxTypeNone
	if req.Sandbox != nil && strings.TrimSpace(req.Sandbox.Type) != "" {
		sandboxType = strings.TrimSpace(req.Sandbox.Type)
	}

	switch sandboxType {
	case "", SandboxTypeNone:
		return executeOnHost(ctx, req, workingDir)
	case SandboxTypeShai:
		return executeInShai(ctx, req, workspaceRoot, workingDir)
	default:
		return nil, nil, fmt.Errorf("unsupported sandbox type %q", sandboxType)
	}
}

func executeOnHost(ctx context.Context, req RunRequest, workingDir string) ([]byte, []byte, error) {
	cmd, err := buildExecCommand(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	cmd.Dir = workingDir
	cmd.Env = buildProcessEnv(req.Env)
	if len(req.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func executeInShai(ctx context.Context, req RunRequest, workspaceRoot string, workingDir string) ([]byte, []byte, error) {
	argv, err := buildExecArgv(req)
	if err != nil {
		return nil, nil, err
	}

	configPath, cleanup, err := prepareShaiConfig(workingDir, workspaceRoot, req.Sandbox)
	if err != nil {
		return nil, nil, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	workdirRel, err := filepath.Rel(workspaceRoot, workingDir)
	if err != nil || strings.HasPrefix(workdirRel, "..") {
		workspaceRoot = workingDir
		workdirRel = "."
	}
	workdirRel = filepath.ToSlash(workdirRel)
	if workdirRel == "" {
		workdirRel = "."
	}

	var stdout, stderr bytes.Buffer
	cfg := shai.SandboxConfig{
		WorkingDir:       workspaceRoot,
		ConfigFile:       configPath,
		ReadWritePaths:   []string{"."},
		PostSetupExec:    &shai.SandboxExec{Command: argv, Env: buildProcessEnvMap(req.Env), Workdir: workdirRel, UseTTY: false},
		Stdout:           &stdout,
		Stderr:           &stderr,
		ShowScriptOutput: false,
	}

	runner, err := shai.NewSandbox(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("create shai sandbox: %w", err)
	}
	defer runner.Close()
	if err := runner.Run(ctx); err != nil {
		return stdout.Bytes(), stderr.Bytes(), err
	}
	return stdout.Bytes(), stderr.Bytes(), nil
}

func buildProcessEnv(env map[string]string) []string {
	merged := buildProcessEnvMap(env)
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, fmt.Sprintf("%s=%s", key, merged[key]))
	}
	return out
}

func buildProcessEnvMap(env map[string]string) map[string]string {
	merged := map[string]string{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		merged[key] = value
	}
	for key, value := range env {
		merged[key] = value
	}
	return merged
}

func buildExecCommand(ctx context.Context, req RunRequest) (*exec.Cmd, error) {
	argv, err := buildExecArgv(req)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, argv[0], argv[1:]...), nil
}

func buildExecArgv(req RunRequest) ([]string, error) {
	if len(req.Command) > 0 {
		return append([]string{}, req.Command...), nil
	}

	run := strings.TrimSpace(req.Run)
	if run == "" {
		return nil, fmt.Errorf("command is required")
	}

	shell := strings.TrimSpace(req.Shell)
	if shell == "" {
		if _, err := exec.LookPath("bash"); err == nil {
			shell = "bash"
		} else {
			shell = "sh"
		}
	}
	if shell == "bash" || shell == "zsh" {
		return []string{shell, "-lc", run}, nil
	}
	return []string{shell, "-c", run}, nil
}

func prepareShaiConfig(workingDir string, workspaceRoot string, sandbox *SandboxInput) (string, func(), error) {
	baseConfigPath, err := findLocalShaiConfig(workingDir, workspaceRoot)
	if err != nil {
		return "", nil, err
	}
	if sandbox == nil || len(sandbox.InlineConfig) == 0 {
		return baseConfigPath, nil, nil
	}

	merged, err := loadYAMLMap(baseConfigPath)
	if err != nil {
		return "", nil, err
	}
	deepMergeMap(merged, sandbox.InlineConfig)

	tmpDir, err := os.MkdirTemp("", "c2-shai-config-*")
	if err != nil {
		return "", nil, fmt.Errorf("create shai config temp dir: %w", err)
	}
	tmpPath := filepath.Join(tmpDir, "config.yaml")
	buf, err := yaml.Marshal(merged)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("marshal merged shai config: %w", err)
	}
	if err := os.WriteFile(tmpPath, buf, 0o644); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("write merged shai config: %w", err)
	}
	return tmpPath, func() { _ = os.RemoveAll(tmpDir) }, nil
}

func findLocalShaiConfig(startDir string, workspaceRoot string) (string, error) {
	dir := strings.TrimSpace(startDir)
	if dir == "" {
		dir = strings.TrimSpace(workspaceRoot)
	}
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd
	}
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		root = dir
	}
	root, _ = filepath.Abs(root)
	dir, _ = filepath.Abs(dir)

	for {
		configPath := filepath.Join(dir, ".shai", "config.yaml")
		if stat, err := os.Stat(configPath); err == nil && !stat.IsDir() {
			return configPath, nil
		}
		if dir == root {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("sandbox.type=shai requires a local .shai/config.yaml")
}

func loadYAMLMap(path string) (map[string]any, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read yaml %s: %w", path, err)
	}
	out := map[string]any{}
	if err := yaml.Unmarshal(buf, &out); err != nil {
		return nil, fmt.Errorf("parse yaml %s: %w", path, err)
	}
	return out, nil
}

func deepMergeMap(dst map[string]any, src map[string]any) {
	for key, value := range src {
		srcMap, srcIsMap := value.(map[string]any)
		dstMap, dstIsMap := dst[key].(map[string]any)
		if srcIsMap && dstIsMap {
			deepMergeMap(dstMap, srcMap)
			dst[key] = dstMap
			continue
		}
		dst[key] = value
	}
}
