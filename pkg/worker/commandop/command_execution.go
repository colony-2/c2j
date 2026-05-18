package commandop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/shellcmd"
)

// CommandExecutionConfig defines the configuration for command execution activities - ALL fields MUST have json tags
type CommandExecutionConfig struct {
	// Optional: working directory for command execution
	WorkingDir string `json:"working_dir"`
	// Optional: shell to use (bash, sh, powershell, cmd)
	Shell string `json:"shell"`
	// Optional: environment variables
	Env map[string]string `json:"env"`
}

// CommandExecutionInput defines the input for command execution activities - ALL fields MUST have json tags
type CommandExecutionInput struct {
	Run              string                `json:"run" validate:"required"`                                                // Required: command to execute
	WorkingDirectory string                `json:"working_directory" default:"{{ context.environment.op.worktree_path }}"` // Optional: override working directory
	Shell            string                `json:"shell" validate:"omitempty,oneof=bash sh powershell cmd"`                // Optional: override shell
	Env              map[string]string     `json:"env"`                                                                    // Optional: additional env vars
	Sandbox          *process.SandboxInput `json:"sandbox,omitempty"`                                                      // Optional: sandbox execution mode
	ContinueOnError  bool                  `json:"continue_on_error"`                                                      // Optional: don't fail on non-zero exit
	Timeout          string                `json:"timeout"`                                                                // Optional: timeout duration (e.g., "30s")
}

// CommandExecutionOutput defines the output from command execution activities - ALL fields MUST have json tags
type CommandExecutionOutput struct {
	Stdout       string `json:"stdout"`        // Standard output
	Stderr       string `json:"stderr"`        // Standard error
	ExitCode     int    `json:"exit_code"`     // Process exit code
	Success      bool   `json:"success"`       // Whether command succeeded
	TimedOut     bool   `json:"timed_out"`     // Whether command timed out
	ErrorMessage string `json:"error_message"` // Error message if failed
}

// Deprecated: CommandExecutionActivity is now a RegisterableOp
func newCommandExecutionActivity() ops.RegisterableOp {
	// Create a new command execution activity that implements RegisterableOp
	return GetOp()
}

// NewCommandExecutionActivity creates a new command execution activity that implements RegisterableOp
func GetOp() ops.RegisterableOp {
	base := ops.NewActivityMappedOpV2[CommandExecutionInput, CommandExecutionOutput](
		ops.OpMetadata{
			Type:             "command_execution",
			Description:      "Executes arbitrary shell commands with GitHub Actions-style configuration",
			Version:          "1.0.0",
			DefaultTimeout:   5 * time.Minute,
			AcceptsArtifacts: true,
		}, execute)
	return commandExecutionOp{RegisterableOp: base}
}

type commandExecutionOp struct {
	ops.RegisterableOp
}

func (o commandExecutionOp) TransformOperationPaths(ctx context.Context, req ops.OperationPathTransformRequest) (ops.OperationPathTransformResult, error) {
	return process.TransformOperationPaths(ctx, req.Input["sandbox"], req.Host)
}

// Execute runs the activity with provided configuration and inputs
func execute(deps ops.OpDependencies, ctx context.Context, input CommandExecutionInput) (CommandExecutionOutput, error) {
	// Validate inputs
	if input.Run == "" {
		return CommandExecutionOutput{}, fmt.Errorf("run command is required")
	}

	config := CommandExecutionConfig{}
	// Determine timeout
	var timeout time.Duration
	if input.Timeout != "" {
		var err error
		timeout, err = time.ParseDuration(input.Timeout)
		if err != nil {
			return CommandExecutionOutput{}, fmt.Errorf("invalid timeout format: %w", err)
		}
		// Create a new context with timeout
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Determine working directory
	workingDir := config.WorkingDir
	if input.WorkingDirectory != "" {
		workingDir = input.WorkingDirectory
	}
	if workingDir == "" {
		workingDir, _ = os.Getwd()
	}

	// Determine shell
	shell := config.Shell
	if input.Shell != "" {
		shell = input.Shell
	}
	if shell == "" {
		shell = getDefaultShell()
	}

	env := map[string]string{}
	for k, v := range config.Env {
		env[k] = v
	}
	for k, v := range input.Env {
		env[k] = v
	}
	workspaceRoot := workingDir
	var mounts []ops.RequiredMount
	if runtimeProvider, ok := deps.(ops.OperationPathRuntimeProvider); ok {
		pathRuntime := runtimeProvider.OperationPathRuntime()
		if process.SandboxType(input.Sandbox) == process.SandboxTypeShai {
			if strings.TrimSpace(pathRuntime.Views.Host.Workdir) != "" {
				workspaceRoot = pathRuntime.Views.Host.Workdir
			}
			if strings.TrimSpace(workingDir) == "" && strings.TrimSpace(pathRuntime.Views.Op.WorktreePath) != "" {
				workingDir = pathRuntime.Views.Op.WorktreePath
			}
		}
		mounts = pathRuntime.Mounts
	}
	stdoutBytes, stderrBytes, err := process.ExecuteProcess(ctx, process.RunRequest{
		WorkspaceRoot:  workspaceRoot,
		WorkingDir:     workingDir,
		Shell:          shell,
		Run:            input.Run,
		Env:            env,
		Sandbox:        input.Sandbox,
		RequiredMounts: mounts,
	})

	// Prepare output
	output := CommandExecutionOutput{
		Stdout:   strings.TrimRight(string(stdoutBytes), "\r\n"),
		Stderr:   strings.TrimRight(string(stderrBytes), "\r\n"),
		ExitCode: 0,
		Success:  true,
		TimedOut: false,
	}

	// Check if context was cancelled due to timeout
	if executionTimedOut(ctx, err) {
		output.TimedOut = true
		output.Success = false
		output.ErrorMessage = "command execution timed out"
		if !input.ContinueOnError {
			return output, fmt.Errorf("command execution timed out")
		}
		return output, nil
	}

	// Handle execution error
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			output.ExitCode = exitError.ExitCode()
		} else {
			output.ExitCode = -1
		}
		output.Success = false
		output.ErrorMessage = err.Error()

		if !input.ContinueOnError {
			return output, fmt.Errorf("command execution failed: %w", err)
		}
	}

	return output, nil
}

func executionTimedOut(ctx context.Context, err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return true
	}
	return errors.Is(context.Cause(ctx), context.DeadlineExceeded)
}

// getDefaultShell returns the default shell based on the operating system
func getDefaultShell() string {
	return shellcmd.DefaultShell()
}
