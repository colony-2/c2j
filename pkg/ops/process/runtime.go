package process

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/shellcmd"
	"github.com/colony-2/shai/pkg/shai"
	yaml "gopkg.in/yaml.v3"
)

const (
	SandboxTypeNone = "none"
	SandboxTypeShai = "shai"

	DefaultShaiWorkdir      = "/src"
	DefaultShaiWorktreePath = "/src/git"
	DefaultShaiInbox        = "/src/inbox"
	DefaultShaiOutbox       = "/src/outbox"
)

type SandboxInput struct {
	Type  string             `json:"type,omitempty" yaml:"type,omitempty"`
	Paths *SandboxPathConfig `json:"paths,omitempty" yaml:"paths,omitempty"`
}

type SandboxPathConfig struct {
	Workdir      SandboxPathMapping `json:"workdir,omitempty" yaml:"workdir,omitempty"`
	WorktreePath SandboxPathMapping `json:"worktree_path,omitempty" yaml:"worktree_path,omitempty"`
	Inbox        SandboxPathMapping `json:"inbox,omitempty" yaml:"inbox,omitempty"`
	Outbox       SandboxPathMapping `json:"outbox,omitempty" yaml:"outbox,omitempty"`
}

type SandboxPathMapping struct {
	Host    string `json:"host,omitempty" yaml:"host,omitempty"`
	Sandbox string `json:"sandbox,omitempty" yaml:"sandbox,omitempty"`
	Mode    string `json:"mode,omitempty" yaml:"mode,omitempty"`
}

type RunRequest struct {
	WorkspaceRoot  string
	WorkingDir     string
	ConfigFile     string
	Shell          string
	Run            string
	Command        []string
	Env            map[string]string
	Stdin          []byte
	Sandbox        *SandboxInput
	RequiredMounts []ops.RequiredMount
}

func ParseSandboxInput(raw interface{}) (*SandboxInput, error) {
	if raw == nil {
		return nil, nil
	}
	buf, err := yaml.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode sandbox input: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(buf))
	dec.KnownFields(true)
	var sandbox SandboxInput
	if err := dec.Decode(&sandbox); err != nil {
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

func SandboxType(sandbox *SandboxInput) string {
	if sandbox == nil || strings.TrimSpace(sandbox.Type) == "" {
		return SandboxTypeNone
	}
	return strings.TrimSpace(sandbox.Type)
}

func TransformOperationPaths(_ context.Context, rawSandbox interface{}, host ops.OperationPaths) (ops.OperationPathTransformResult, error) {
	if ContainsOpVisibleSentinel(rawSandbox) {
		return ops.OperationPathTransformResult{}, fmt.Errorf("op-visible path mapping failed: sandbox config cannot reference context.environment.op.*")
	}
	sandbox, err := ParseSandboxInput(rawSandbox)
	if err != nil {
		return ops.OperationPathTransformResult{}, err
	}
	sandboxType := SandboxType(sandbox)
	if sandboxType == SandboxTypeShai && goruntime.GOOS == "windows" {
		return ops.OperationPathTransformResult{}, fmt.Errorf("op-visible path mapping failed: sandbox %q does not support Windows hosts", sandboxType)
	}

	paths, err := resolveSandboxPathConfig(sandbox, host)
	if err != nil {
		return ops.OperationPathTransformResult{}, err
	}

	views := ops.OperationPathViews{Host: host}
	switch sandboxType {
	case SandboxTypeNone:
		views.Op = ops.OperationPaths{
			Workdir:      paths.Workdir.Host,
			WorktreePath: paths.WorktreePath.Host,
			Inbox:        paths.Inbox.Host,
			Outbox:       paths.Outbox.Host,
		}
	case SandboxTypeShai:
		views.Op = ops.OperationPaths{
			Workdir:      paths.Workdir.Sandbox,
			WorktreePath: paths.WorktreePath.Sandbox,
			Inbox:        paths.Inbox.Sandbox,
			Outbox:       paths.Outbox.Sandbox,
		}
	default:
		return ops.OperationPathTransformResult{}, fmt.Errorf("unsupported sandbox type %q", sandboxType)
	}

	result := ops.OperationPathTransformResult{
		Runtime: ops.OperationPathRuntime{
			Views: views,
		},
		Replacements: map[string]string{
			contextual.OpWorkdirPathSentinel:    views.Op.Workdir,
			contextual.OpWorktreePathSentinel:   views.Op.WorktreePath,
			contextual.OpArtifactInboxSentinel:  views.Op.Inbox,
			contextual.OpArtifactOutboxSentinel: views.Op.Outbox,
		},
	}
	if sandboxType == SandboxTypeShai {
		mounts, err := mountsFromPathConfig(paths)
		if err != nil {
			return ops.OperationPathTransformResult{}, err
		}
		result.Runtime.Mounts = mounts
	}
	return result, nil
}

func resolveSandboxPathConfig(sandbox *SandboxInput, host ops.OperationPaths) (SandboxPathConfig, error) {
	sandboxType := SandboxType(sandbox)
	paths := defaultSandboxPathConfig(host, sandboxType)
	if sandbox != nil && sandbox.Paths != nil {
		mergePathMapping(&paths.Workdir, sandbox.Paths.Workdir)
		mergePathMapping(&paths.WorktreePath, sandbox.Paths.WorktreePath)
		mergePathMapping(&paths.Inbox, sandbox.Paths.Inbox)
		mergePathMapping(&paths.Outbox, sandbox.Paths.Outbox)
	}
	if err := validatePathMapping("workdir", paths.Workdir, sandboxType); err != nil {
		return SandboxPathConfig{}, err
	}
	if err := validatePathMapping("worktree_path", paths.WorktreePath, sandboxType); err != nil {
		return SandboxPathConfig{}, err
	}
	if err := validatePathMapping("inbox", paths.Inbox, sandboxType); err != nil {
		return SandboxPathConfig{}, err
	}
	if err := validatePathMapping("outbox", paths.Outbox, sandboxType); err != nil {
		return SandboxPathConfig{}, err
	}
	return paths, nil
}

func defaultSandboxPathConfig(host ops.OperationPaths, sandboxType string) SandboxPathConfig {
	paths := SandboxPathConfig{
		Workdir: SandboxPathMapping{
			Host:    host.Workdir,
			Sandbox: host.Workdir,
			Mode:    ops.MountModeReadWrite,
		},
		WorktreePath: SandboxPathMapping{
			Host:    host.WorktreePath,
			Sandbox: host.WorktreePath,
			Mode:    ops.MountModeReadWrite,
		},
		Inbox: SandboxPathMapping{
			Host:    host.Inbox,
			Sandbox: host.Inbox,
			Mode:    ops.MountModeReadWrite,
		},
		Outbox: SandboxPathMapping{
			Host:    host.Outbox,
			Sandbox: host.Outbox,
			Mode:    ops.MountModeReadWrite,
		},
	}
	if sandboxType == SandboxTypeShai {
		paths.Workdir.Sandbox = DefaultShaiWorkdir
		paths.WorktreePath.Sandbox = DefaultShaiWorktreePath
		paths.Inbox.Sandbox = DefaultShaiInbox
		paths.Outbox.Sandbox = DefaultShaiOutbox
	}
	return paths
}

func mergePathMapping(dst *SandboxPathMapping, src SandboxPathMapping) {
	if strings.TrimSpace(src.Host) != "" {
		dst.Host = strings.TrimSpace(src.Host)
	}
	if strings.TrimSpace(src.Sandbox) != "" {
		dst.Sandbox = strings.TrimSpace(src.Sandbox)
	}
	if strings.TrimSpace(src.Mode) != "" {
		dst.Mode = strings.TrimSpace(src.Mode)
	}
}

func validatePathMapping(name string, mapping SandboxPathMapping, sandboxType string) error {
	if strings.TrimSpace(mapping.Host) == "" {
		return fmt.Errorf("op-visible path mapping failed: %s host path is required", name)
	}
	if !filepath.IsAbs(mapping.Host) {
		return fmt.Errorf("op-visible path mapping failed: %s host path must be absolute", name)
	}
	mode := strings.TrimSpace(mapping.Mode)
	if mode == "" {
		mode = ops.MountModeReadWrite
	}
	if mode != ops.MountModeReadOnly && mode != ops.MountModeReadWrite {
		return fmt.Errorf("op-visible path mapping failed: %s mode must be %q or %q", name, ops.MountModeReadOnly, ops.MountModeReadWrite)
	}
	if sandboxType == SandboxTypeShai {
		if strings.TrimSpace(mapping.Sandbox) == "" {
			return fmt.Errorf("op-visible path mapping failed: sandbox %q did not provide a %s sandbox path", sandboxType, name)
		}
		if !path.IsAbs(mapping.Sandbox) {
			return fmt.Errorf("op-visible path mapping failed: %s sandbox path must be absolute", name)
		}
	}
	return nil
}

func mountsFromPathConfig(paths SandboxPathConfig) ([]ops.RequiredMount, error) {
	return dedupeMounts([]ops.RequiredMount{
		mountFromMapping(paths.Workdir),
		mountFromMapping(paths.WorktreePath),
		mountFromMapping(paths.Inbox),
		mountFromMapping(paths.Outbox),
	})
}

func mountFromMapping(mapping SandboxPathMapping) ops.RequiredMount {
	mode := strings.TrimSpace(mapping.Mode)
	if mode == "" {
		mode = ops.MountModeReadWrite
	}
	return ops.RequiredMount{
		Source: mapping.Host,
		Target: mapping.Sandbox,
		Mode:   mode,
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

	switch SandboxType(req.Sandbox) {
	case SandboxTypeNone:
		return executeOnHost(ctx, req, workingDir)
	case SandboxTypeShai:
		if goruntime.GOOS == "windows" {
			return nil, nil, fmt.Errorf("sandbox.type=shai does not support Windows hosts")
		}
		return executeInShai(ctx, req, workspaceRoot, workingDir)
	default:
		return nil, nil, fmt.Errorf("unsupported sandbox type %q", SandboxType(req.Sandbox))
	}
}

func executeOnHost(ctx context.Context, req RunRequest, workingDir string) ([]byte, []byte, error) {
	cmd, err := buildExecCommand(req)
	if err != nil {
		return nil, nil, err
	}
	cmd.Dir = workingDir
	cmd.Env = BuildProcessEnv(req.Env)
	if len(req.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	configureProcessTree(cmd)
	if err := cmd.Start(); err != nil {
		return stdout.Bytes(), stderr.Bytes(), err
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case err := <-waitCh:
		return stdout.Bytes(), stderr.Bytes(), err
	case <-ctx.Done():
		terminateProcessTree(cmd)
		select {
		case <-waitCh:
		case <-time.After(processTerminationGrace):
			killProcessTree(cmd)
			<-waitCh
		}
		cause := context.Cause(ctx)
		if cause == nil {
			cause = ctx.Err()
		}
		return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("process canceled: %w", cause)
	}
}

func executeInShai(ctx context.Context, req RunRequest, workspaceRoot string, workingDir string) ([]byte, []byte, error) {
	argv, err := buildExecArgv(req)
	if err != nil {
		return nil, nil, err
	}

	configPath := strings.TrimSpace(req.ConfigFile)
	if configPath == "" {
		configPath = findLocalShaiConfig(workingDir, workspaceRoot)
	}
	sandboxWorkingDir := sandboxWorkingDir(workspaceRoot, workingDir)
	if sandboxWorkingDir == "" {
		sandboxWorkingDir = DefaultShaiWorkdir
	}
	if sandboxWorkingDir != DefaultShaiWorkdir {
		argv = wrapArgvWithWorkdir(argv, sandboxWorkingDir)
	}

	appendSet, err := shaiAppendResourceSet(req.RequiredMounts, workspaceRoot, DefaultShaiWorkdir)
	if err != nil {
		return nil, nil, err
	}

	var stdout, stderr bytes.Buffer
	cfg := shai.SandboxConfig{
		WorkingDir:          workspaceRoot,
		ConfigFile:          configPath,
		ReadWritePaths:      []string{"."},
		AppendResourceSet:   appendSet,
		PostSetupExec:       &shai.SandboxExec{Command: argv, Env: BuildProcessEnvMap(req.Env), Workdir: sandboxWorkingDir, UseTTY: false},
		Stdout:              &stdout,
		Stderr:              &stderr,
		ShowScriptOutput:    false,
		ShowProgress:        false,
		GracefulStopTimeout: 0,
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

func BuildProcessEnv(env map[string]string) []string {
	merged := BuildProcessEnvMap(env)
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

func BuildProcessEnvMap(env map[string]string) map[string]string {
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

func buildExecCommand(req RunRequest) (*exec.Cmd, error) {
	argv, err := buildExecArgv(req)
	if err != nil {
		return nil, err
	}
	return exec.Command(argv[0], argv[1:]...), nil
}

func buildExecArgv(req RunRequest) ([]string, error) {
	if len(req.Command) > 0 {
		return append([]string{}, req.Command...), nil
	}

	run := strings.TrimSpace(req.Run)
	if run == "" {
		return nil, fmt.Errorf("command is required")
	}

	return shellcmd.BuildArgv(req.Shell, run)
}

func findLocalShaiConfig(startDir string, workspaceRoot string) string {
	dir := strings.TrimSpace(startDir)
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		root = dir
	}
	root, _ = filepath.Abs(root)
	if dir == "" || path.IsAbs(dir) && !filepath.IsAbs(dir) {
		dir = root
	} else {
		dir, _ = filepath.Abs(dir)
		if rel, err := filepath.Rel(root, dir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			dir = root
		}
	}

	for {
		configPath := filepath.Join(dir, ".shai", "config.yaml")
		if stat, err := os.Stat(configPath); err == nil && !stat.IsDir() {
			return configPath
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
	return filepath.Join(root, ".shai", "config.yaml")
}

func sandboxWorkingDir(workspaceRoot string, workingDir string) string {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return DefaultShaiWorkdir
	}
	if strings.HasPrefix(workingDir, "/") && !filepath.IsAbs(workingDir) {
		return path.Clean(workingDir)
	}
	if strings.HasPrefix(workingDir, "/") {
		rootAbs, _ := filepath.Abs(workspaceRoot)
		wdAbs, _ := filepath.Abs(workingDir)
		if rel, err := filepath.Rel(rootAbs, wdAbs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			rel = filepath.ToSlash(rel)
			if rel == "." || rel == "" {
				return DefaultShaiWorkdir
			}
			return path.Join(DefaultShaiWorkdir, rel)
		}
		return path.Clean(workingDir)
	}
	if workingDir == "." {
		return DefaultShaiWorkdir
	}
	return path.Join(DefaultShaiWorkdir, filepath.ToSlash(workingDir))
}

func shaiAppendResourceSet(mounts []ops.RequiredMount, workspaceRoot string, workspaceTarget string) (*shai.ResourceSet, error) {
	filtered, err := filterWorkspaceCoveredMounts(mounts, workspaceRoot, workspaceTarget)
	if err != nil {
		return nil, err
	}
	filtered, err = dedupeMounts(filtered)
	if err != nil {
		return nil, err
	}
	if len(filtered) == 0 {
		return nil, nil
	}
	out := &shai.ResourceSet{Mounts: make([]shai.Mount, 0, len(filtered))}
	for _, mount := range filtered {
		out.Mounts = append(out.Mounts, shai.Mount{
			Source: mount.Source,
			Target: mount.Target,
			Mode:   mount.Mode,
		})
	}
	return out, nil
}

func filterWorkspaceCoveredMounts(mounts []ops.RequiredMount, workspaceRoot string, workspaceTarget string) ([]ops.RequiredMount, error) {
	out := make([]ops.RequiredMount, 0, len(mounts))
	rootAbs, _ := filepath.Abs(workspaceRoot)
	for _, mount := range mounts {
		source := strings.TrimSpace(mount.Source)
		target := path.Clean(strings.TrimSpace(mount.Target))
		if source == "" || target == "." {
			continue
		}
		sourceAbs, _ := filepath.Abs(source)
		if target == workspaceTarget && sourceAbs == rootAbs {
			continue
		}
		rel, err := filepath.Rel(rootAbs, sourceAbs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			coveredTarget := workspaceTarget
			if rel != "." && rel != "" {
				coveredTarget = path.Join(workspaceTarget, filepath.ToSlash(rel))
			}
			if target == coveredTarget {
				continue
			}
		}
		out = append(out, ops.RequiredMount{
			Source: sourceAbs,
			Target: target,
			Mode:   normalizeMountMode(mount.Mode),
		})
	}
	return out, nil
}

func dedupeMounts(mounts []ops.RequiredMount) ([]ops.RequiredMount, error) {
	byTarget := map[string]ops.RequiredMount{}
	order := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		mount.Source = strings.TrimSpace(mount.Source)
		mount.Target = path.Clean(strings.TrimSpace(mount.Target))
		mount.Mode = normalizeMountMode(mount.Mode)
		if mount.Source == "" || mount.Target == "." {
			continue
		}
		existing, ok := byTarget[mount.Target]
		if !ok {
			byTarget[mount.Target] = mount
			order = append(order, mount.Target)
			continue
		}
		if existing.Source == mount.Source && existing.Mode == mount.Mode {
			continue
		}
		return nil, fmt.Errorf("op-visible path mapping failed: duplicate mount target %q maps to both %q and %q", mount.Target, existing.Source, mount.Source)
	}
	out := make([]ops.RequiredMount, 0, len(order))
	for _, target := range order {
		out = append(out, byTarget[target])
	}
	return out, nil
}

func normalizeMountMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return ops.MountModeReadWrite
	}
	return mode
}

func wrapArgvWithWorkdir(argv []string, workdir string) []string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		parts = append(parts, shellQuote(arg))
	}
	return []string{"sh", "-lc", "cd " + shellQuote(workdir) + " && exec " + strings.Join(parts, " ")}
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func ContainsOpVisibleSentinel(value interface{}) bool {
	switch v := value.(type) {
	case string:
		return strings.Contains(v, contextual.OpWorkdirPathSentinel) ||
			strings.Contains(v, contextual.OpWorktreePathSentinel) ||
			strings.Contains(v, contextual.OpArtifactInboxSentinel) ||
			strings.Contains(v, contextual.OpArtifactOutboxSentinel)
	case map[string]interface{}:
		for key, item := range v {
			if ContainsOpVisibleSentinel(key) || ContainsOpVisibleSentinel(item) {
				return true
			}
		}
	case []interface{}:
		for _, item := range v {
			if ContainsOpVisibleSentinel(item) {
				return true
			}
		}
	}
	return false
}
