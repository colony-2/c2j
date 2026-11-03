package commandop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/executiondiag"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/shellcmd"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// CommandExecutionConfig defines the configuration for command execution activities - ALL fields MUST have json tags
type CommandExecutionConfig struct {
	// Optional: working directory for command execution
	WorkingDir string `json:"working_dir"`
	// Optional: shell to use (bash, sh, powershell, cmd)
	Shell string `json:"shell"`
	// Optional: environment variables
	Env map[string]string `json:"env" secret:"env"`
}

// CommandExecutionInput defines the input for command execution activities - ALL fields MUST have json tags
type CommandExecutionInput struct {
	CaptureDiagnostics string            `json:"capture_diagnostics,omitempty" validate:"omitempty,oneof=failure always"`
	Run                string            `json:"run" validate:"required"`                                                // Required: command to execute
	WorkingDirectory   string            `json:"working_directory" default:"{{ context.environment.op.worktree_path }}"` // Optional: override working directory
	Shell              string            `json:"shell" validate:"omitempty,oneof=bash sh powershell cmd"`                // Optional: override shell
	Env                map[string]string `json:"env" secret:"env"`                                                       // Optional: additional env vars
	ContinueOnError    bool              `json:"continue_on_error"`                                                      // Optional: don't fail on non-zero exit
	Timeout            string            `json:"timeout"`                                                                // Optional: timeout duration (e.g., "30s")
}

// CommandExecutionOutput defines the output from command execution activities - ALL fields MUST have json tags
type CommandExecutionOutput struct {
	StdoutTruncated  bool                     `json:"stdout_truncated,omitempty"`
	StderrTruncated  bool                     `json:"stderr_truncated,omitempty"`
	Diagnostics      map[string]artifacts.Ref `json:"diagnostics,omitempty"`
	DiagnosticErrors []string                 `json:"diagnostic_errors,omitempty"`
	Stdout           string                   `json:"stdout"`        // Standard output
	Stderr           string                   `json:"stderr"`        // Standard error
	ExitCode         int                      `json:"exit_code"`     // Process exit code
	Success          bool                     `json:"success"`       // Whether command succeeded
	TimedOut         bool                     `json:"timed_out"`     // Whether command timed out
	ErrorMessage     string                   `json:"error_message"` // Error message if failed
}

// Deprecated: CommandExecutionActivity is now a RegisterableOp
func newCommandExecutionActivity() ops.RegisterableOp {
	// Create a new command execution activity that implements RegisterableOp
	return GetOp()
}

// NewCommandExecutionActivity creates a new command execution activity that implements RegisterableOp
func GetOp() ops.RegisterableOp {
	return ops.NewActivityMappedOpV2[CommandExecutionInput, CommandExecutionOutput](
		ops.OpMetadata{
			Type:             "command_execution",
			Description:      "Executes arbitrary shell commands with GitHub Actions-style configuration",
			Version:          "1.0.0",
			AcceptsArtifacts: true,
		}, execute)
}

// Execute runs the activity with provided configuration and inputs
func execute(deps ops.OpDependencies, ctx context.Context, input CommandExecutionInput) (output CommandExecutionOutput, runErr error) {
	report := executiondiag.New(ctx, deps)
	defer report.Close()
	report.Capture.Request = process.RunRequest{Run: input.Run, Shell: input.Shell, WorkingDir: input.WorkingDirectory}
	defer func() {
		redactor := secrets.RedactorFromContext(ctx)
		output.ErrorMessage = executiondiag.Short(redactor.Redact(output.ErrorMessage))
		output.Stdout, output.StdoutTruncated = executiondiag.Preview(output.Stdout, report.Capture.Truncated(0), report.Name)
		output.Stderr, output.StderrTruncated = executiondiag.Preview(output.Stderr, report.Capture.Truncated(1), report.Name)
		if budgetErr := executiondiag.CheckOutput(output); budgetErr != nil {
			runErr = errors.Join(runErr, budgetErr)
			output.Success = false
		}
		if runErr != nil || input.CaptureDiagnostics == "always" || output.StdoutTruncated || output.StderrTruncated {
			exit := output.ExitCode
			if !report.Capture.Launched || output.TimedOut {
				exit = -1
			}
			details := report.Publish(runErr, exit, output.TimedOut)
			output.Diagnostics, output.DiagnosticErrors = details.Diagnostics, details.DiagnosticErrors
			if len(details.Diagnostics) == 0 {
				// Rebuild from captured bytes rather than truncating a prior notice.
				output.Stdout, output.StdoutTruncated = executiondiag.Preview(string(report.Capture.Bytes(0)), output.StdoutTruncated, "")
				output.Stderr, output.StderrTruncated = executiondiag.Preview(string(report.Capture.Bytes(1)), output.StderrTruncated, "")
			}
			if runErr != nil {
				runErr = &commandFailure{cause: runErr, message: executiondiag.Short(redactor.Redact(runErr.Error())), details: details}
			}
		}
	}()
	if input.CaptureDiagnostics != "" && input.CaptureDiagnostics != "failure" && input.CaptureDiagnostics != "always" {
		return output, fmt.Errorf("invalid capture_diagnostics: expected failure or always")
	}
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
	env = jobcontext.MergeProtectedEnv(env, deps.ProtectedEnv())
	_, _, err := process.ExecuteProcess(ctx, report.Prepare(process.RunRequest{
		WorkingDir: workingDir,
		Shell:      shell,
		Run:        input.Run,
		Env:        env,
	}))

	// Prepare output
	output = CommandExecutionOutput{
		Stdout:   strings.TrimRight(string(report.Capture.Bytes(0)), "\r\n"),
		Stderr:   strings.TrimRight(string(report.Capture.Bytes(1)), "\r\n"),
		ExitCode: 0,
		Success:  true,
		TimedOut: false,
	}

	// Check if context was cancelled due to timeout
	if executionTimedOut(ctx, err) {
		output.TimedOut = true
		output.ExitCode = -1
		output.Success = false
		output.ErrorMessage = "command execution timed out"
		if !input.ContinueOnError {
			cause := errors.Join(err, context.Cause(ctx), context.DeadlineExceeded)
			var nativeTimeout *jobdb.TimeoutError
			if !errors.As(cause, &nativeTimeout) {
				cause = errors.Join(cause, &jobdb.TimeoutError{Payload: jobdb.TimeoutPayload{
					Kind: "task", Scope: jobdb.TimeoutScopeInvocation, After: jobdb.Duration(timeout),
					Code: "timeout_invocation", Message: output.ErrorMessage, Retryable: true,
				}})
			}
			return output, &commandFailure{cause: cause, message: output.ErrorMessage}
		}
		return output, nil
	}

	// Handle execution error
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			output.ExitCode = exitError.ExitCode()
		} else {
			output.ExitCode = -1
		}
		output.Success = false
		output.ErrorMessage = err.Error()

		if !input.ContinueOnError {
			return output, fmt.Errorf("command execution failed: %w", errors.Join(err, context.Cause(ctx)))
		}
	}

	return output, nil
}

func executionTimedOut(ctx context.Context, err error) bool {
	var timeout *jobdb.TimeoutError
	if errors.As(err, &timeout) || errors.As(context.Cause(ctx), &timeout) {
		return true
	}
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
