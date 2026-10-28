package process

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/shellcmd"
)

type RunRequest struct {
	// CleanEnv is for preparation subprocesses that must not inherit worker state.
	// Commands in this mode must use an absolute executable path.
	CleanEnv      bool
	WorkspaceRoot string
	WorkingDir    string
	Shell         string
	Run           string
	Command       []string
	Env           map[string]string
	Stdin         []byte
}

func ExecuteProcess(ctx context.Context, req RunRequest) (stdout, stderr []byte, runErr error) {
	if scope := gitprofile.FromContext(ctx); scope != nil {
		release := scope.RetainRedaction()
		defer release()
		defer func() {
			stdout = []byte(scope.Redact(string(stdout)))
			stderr = []byte(scope.Redact(string(stderr)))
			runErr = scope.Error(runErr)
		}()
	}
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

	return executeOnHost(ctx, req, workingDir)
}

func executeOnHost(ctx context.Context, req RunRequest, workingDir string) ([]byte, []byte, error) {
	if !req.CleanEnv {
		req.Env = withContextEnv(ctx, req.Env)
	}
	cmd, err := buildExecCommand(req)
	if err != nil {
		return nil, nil, err
	}
	cmd.Dir = workingDir
	cmd.Env = BuildProcessEnv(req.Env)
	if req.CleanEnv {
		if !filepath.IsAbs(cmd.Path) {
			return nil, nil, fmt.Errorf("clean environment requires an absolute executable")
		}
		cmd.Env = []string{}
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	unset, _ := ctx.Value(unsetEnvKey{}).([]string)
	for _, name := range unset {
		filtered := cmd.Env[:0]
		for _, entry := range cmd.Env {
			if !strings.HasPrefix(entry, name+"=") {
				filtered = append(filtered, entry)
			}
		}
		cmd.Env = filtered
	}
	if len(req.Stdin) > 0 {
		// Input bytes are independent of the protected execution environment.
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}
	if !req.CleanEnv {
		cmd.Env = gitprofile.Env(ctx, cmd.Env)
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
	if req.CleanEnv && !filepath.IsAbs(argv[0]) {
		return nil, fmt.Errorf("clean environment requires an absolute executable")
	}
	if !strings.ContainsRune(argv[0], os.PathSeparator) && req.Env["PATH"] != "" {
		for _, dir := range filepath.SplitList(req.Env["PATH"]) {
			if dir == "" {
				continue
			}
			p := filepath.Join(dir, argv[0])
			if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
				argv[0] = p
				break
			}
		}
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

// WithToolPath binds tools to one invocation, without mutating os.Environ.
func WithToolPath(ctx context.Context, path string) context.Context {
	return WithToolEnvironment(ctx, path, nil)
}

type toolPathKey struct{}

type toolEnvironment struct {
	path      string
	variables map[string]string
}

// WithToolEnvironment binds native activation and executable selection only to
// this invocation. Explicit op variables override activation scalar defaults.
func WithToolEnvironment(ctx context.Context, path string, variables map[string]string) context.Context {
	copy := make(map[string]string, len(variables))
	for k, v := range variables {
		copy[k] = v
	}
	return context.WithValue(ctx, toolPathKey{}, toolEnvironment{path, copy})
}

func withContextEnv(ctx context.Context, env map[string]string) map[string]string {
	tools, ok := ctx.Value(toolPathKey{}).(toolEnvironment)
	if !ok {
		return env
	}
	out := BuildProcessEnvMap(nil)
	basePath := out["PATH"]
	for k, v := range tools.variables {
		out[k] = v
	}
	for k, v := range env {
		out[k] = v
	}
	if path, ok := env["PATH"]; ok {
		basePath = path
	}
	var paths []string
	for _, path := range []string{tools.path, tools.variables["PATH"], basePath} {
		if path != "" {
			paths = append(paths, path)
		}
	}
	out["PATH"] = strings.Join(paths, string(os.PathListSeparator))
	return out
}

type unsetEnvKey struct{}

// WithUnsetEnv suppresses omitted secret variables after inherited and explicit
// environment values have been merged, without changing the runner environment.
func WithUnsetEnv(ctx context.Context, names []string) context.Context {
	return context.WithValue(ctx, unsetEnvKey{}, append([]string(nil), names...))
}
