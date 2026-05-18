package shellcmd

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// DefaultShell returns the shell used when callers do not request one.
func DefaultShell() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	if _, err := exec.LookPath("bash"); err == nil {
		return "bash"
	}
	return "sh"
}

// BuildArgv returns the argv needed to execute script with shell.
func BuildArgv(shell string, script string) ([]string, error) {
	shell = strings.TrimSpace(shell)
	if shell == "" {
		shell = DefaultShell()
	}
	script = strings.TrimSpace(script)
	if script == "" {
		return nil, fmt.Errorf("command is required")
	}

	switch shellBase(shell) {
	case "bash", "zsh", "sh":
		return []string{shell, "-c", script}, nil
	case "cmd":
		return []string{shell, "/C", script}, nil
	case "powershell", "pwsh":
		return []string{shell, "-Command", script}, nil
	default:
		return []string{shell, "-c", script}, nil
	}
}

func shellBase(shell string) string {
	shell = strings.TrimSpace(shell)
	if idx := strings.LastIndexAny(shell, `/\`); idx >= 0 {
		shell = shell[idx+1:]
	}
	shell = strings.ToLower(shell)
	if strings.HasSuffix(shell, ".exe") {
		shell = strings.TrimSuffix(shell, ".exe")
	}
	return shell
}
