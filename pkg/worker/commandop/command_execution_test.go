package commandop

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	ops "github.com/colony-2/c2j/pkg/core/ops"
)

func TestCommandExecutionActivity_GetMetadata(t *testing.T) {
	activity := GetOp()
	metadata := activity.GetMetadata()

	if metadata.Type != "command_execution" {
		t.Errorf("Expected type 'command_execution', got '%s'", metadata.Type)
	}
	if metadata.Version != "1.0.0" {
		t.Errorf("Expected version '1.0.0', got '%s'", metadata.Version)
	}
	if metadata.DefaultTimeout != 5*time.Minute {
		t.Errorf("Expected default timeout 5m, got %v", metadata.DefaultTimeout)
	}
}

func TestCommandExecutionActivity_Execute_SimpleCommand(t *testing.T) {
	ctx := context.Background()

	input := CommandExecutionInput{
		Run: "echo 'Hello, World!'",
	}

	output, err := execute(ops.NewOpDependenciesBuilder().Build(), ctx, input)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !output.Success {
		t.Errorf("Expected success=true, got false")
	}
	if output.ExitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", output.ExitCode)
	}
	expectedOutput := "Hello, World!"
	if !strings.Contains(output.Stdout, expectedOutput) {
		t.Errorf("Expected stdout to contain '%s', got '%s'", expectedOutput, output.Stdout)
	}
	if output.Stderr != "" {
		t.Errorf("Expected empty stderr, got '%s'", output.Stderr)
	}
}

func TestCommandExecutionActivity_Execute_WithEnvironmentVariables(t *testing.T) {
	ctx := context.Background()

	input := CommandExecutionInput{
		Run: "echo $CONFIG_VAR $INPUT_VAR",
		Env: map[string]string{
			"CONFIG_VAR": "config_value",
			"INPUT_VAR":  "input_value",
		},
	}

	output, err := execute(ops.NewOpDependenciesBuilder().Build(), ctx, input)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !output.Success {
		t.Errorf("Expected success=true, got false")
	}
	expectedOutput := "config_value input_value"
	if !strings.Contains(output.Stdout, expectedOutput) {
		t.Errorf("Expected stdout to contain '%s', got '%s'", expectedOutput, output.Stdout)
	}
}

func TestCommandExecutionActivity_Execute_WithWorkingDirectory(t *testing.T) {
	ctx := context.Background()

	// Use temp directory for testing
	tempDir := t.TempDir()

	input := CommandExecutionInput{
		Run:              "pwd",
		WorkingDirectory: tempDir,
	}

	output, err := execute(ops.NewOpDependenciesBuilder().Build(), ctx, input)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !output.Success {
		t.Errorf("Expected success=true, got false")
	}
	if !strings.Contains(output.Stdout, tempDir) {
		t.Errorf("Expected stdout to contain '%s', got '%s'", tempDir, output.Stdout)
	}
}

func TestCommandExecutionActivity_Execute_FailedCommand(t *testing.T) {
	ctx := context.Background()

	input := CommandExecutionInput{
		Run: "exit 1",
	}

	output, err := execute(ops.NewOpDependenciesBuilder().Build(), ctx, input)
	if err == nil {
		t.Error("Expected error for failed command")
	}

	if output.Success {
		t.Errorf("Expected success=false, got true")
	}
	if output.ExitCode != 1 {
		t.Errorf("Expected exit code 1, got %d", output.ExitCode)
	}
}

func TestCommandExecutionActivity_Execute_ContinueOnError(t *testing.T) {
	ctx := context.Background()

	input := CommandExecutionInput{
		Run:             "exit 1",
		ContinueOnError: true,
	}

	output, err := execute(ops.NewOpDependenciesBuilder().Build(), ctx, input)
	if err != nil {
		t.Errorf("Expected no error with continue_on_error=true, got: %v", err)
	}

	if output.Success {
		t.Errorf("Expected success=false, got true")
	}
	if output.ExitCode != 1 {
		t.Errorf("Expected exit code 1, got %d", output.ExitCode)
	}
}

func TestCommandExecutionActivity_Execute_Timeout(t *testing.T) {
	// Skip on Windows as sleep command may not be available
	if runtime.GOOS == "windows" {
		t.Skip("Skipping timeout test on Windows")
	}

	ctx := context.Background()

	input := CommandExecutionInput{
		Run:     "sleep 5",
		Timeout: "100ms",
	}

	output, err := execute(ops.NewOpDependenciesBuilder().Build(), ctx, input)
	if err == nil {
		t.Error("Expected timeout error")
	}

	if !output.TimedOut {
		t.Errorf("Expected timed_out=true, got false")
	}
	if output.Success {
		t.Errorf("Expected success=false, got true")
	}
}

func TestCommandExecutionActivity_Execute_MissingCommand(t *testing.T) {
	ctx := context.Background()

	input := CommandExecutionInput{
		Run: "",
	}

	_, err := execute(ops.NewOpDependenciesBuilder().Build(), ctx, input)
	if err == nil {
		t.Error("Expected error for missing command")
	}
	if !strings.Contains(err.Error(), "run command is required") {
		t.Errorf("Expected error about missing command, got: %v", err)
	}
}

func TestCommandExecutionActivity_Execute_InvalidTimeout(t *testing.T) {
	ctx := context.Background()

	input := CommandExecutionInput{
		Run:     "echo test",
		Timeout: "invalid",
	}

	_, err := execute(ops.NewOpDependenciesBuilder().Build(), ctx, input)
	if err == nil {
		t.Error("Expected error for invalid timeout")
	}
	if !strings.Contains(err.Error(), "invalid timeout format") {
		t.Errorf("Expected error about invalid timeout, got: %v", err)
	}
}

func TestCommandExecutionActivity_Execute_ShellOverride(t *testing.T) {
	ctx := context.Background()

	// Test with bash if available
	input := CommandExecutionInput{
		Run:   "echo 'Shell test'",
		Shell: "bash", // Override with bash if available
	}

	output, err := execute(ops.NewOpDependenciesBuilder().Build(), ctx, input)
	if err != nil {
		// If bash is not available, skip this test
		if strings.Contains(err.Error(), "bash") {
			t.Skip("Bash not available, skipping shell override test")
		}
		t.Fatalf("Unexpected error: %v", err)
	}

	if !output.Success {
		t.Errorf("Expected success=true, got false")
	}
	if !strings.Contains(output.Stdout, "Shell test") {
		t.Errorf("Expected stdout to contain 'Shell test', got '%s'", output.Stdout)
	}
}
