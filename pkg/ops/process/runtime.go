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
	"github.com/colony-2/c2j/pkg/shellcmd"
)

type RunRequest struct {
	WorkspaceRoot string
	WorkingDir    string
	Shell         string
	Run           string
	Command       []string
	Env           map[string]string
	Stdin         []byte
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

	return executeOnHost(ctx, req, workingDir)
}

func executeOnHost(ctx context.Context, req RunRequest, workingDir string) ([]byte, []byte, error) {
	req.Env = withContextEnv(ctx, req.Env)
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
	return context.WithValue(ctx, toolPathKey{}, path)
}

type toolPathKey struct{}

func withContextEnv(ctx context.Context, env map[string]string) map[string]string {
	path, _ := ctx.Value(toolPathKey{}).(string)
	if path == "" {
		return env
	}
	out := BuildProcessEnvMap(env)
	out["PATH"] = path + string(os.PathListSeparator) + out["PATH"]
	return out
}
