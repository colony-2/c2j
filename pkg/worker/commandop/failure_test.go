package commandop

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/colony-2/c2j/pkg/ops/executiondiag"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type diagnosticJobTool struct{ ops.JobTool }

func (diagnosticJobTool) GetJobKey() jobdb.JobKey {
	return jobdb.JobKey{TenantId: "test", JobId: "command-job"}
}
func (diagnosticJobTool) TaskOrdinal() int64 { return 17 }

type rejectingArtifacts struct{ ops.OpDependencies }

func (rejectingArtifacts) AddOutputArtifact(jobdb.Artifact) error {
	return errors.New("artifact collection failed")
}

func TestCommandFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name               string
		input              CommandExecutionInput
		stdout, stderr     string
		exit               int
		timedOut, canceled bool
	}{
		{name: "exit", input: CommandExecutionInput{Run: `printf 'checking tools\n'; printf 'Required tool unavailable: go\n' >&2; exit 7`}, stdout: "checking tools\n", stderr: "Required tool unavailable: go\n", exit: 7},
		{name: "empty", input: CommandExecutionInput{Run: "exit 7"}, exit: 7},
		{name: "missing executable", input: CommandExecutionInput{Run: "echo unused", Shell: "/nonexistent/c2j-test-shell"}, exit: -1},
		{name: "timeout", input: CommandExecutionInput{Run: "printf 'partial\n'; printf 'error\n' >&2; sleep 5", Timeout: "200ms"}, stdout: "partial\n", stderr: "error\n", exit: -1, timedOut: true},
		{name: "cancellation", input: CommandExecutionInput{Run: "sleep 5"}, exit: -1, canceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := ops.NewOpDependenciesBuilder().WithJobTool(diagnosticJobTool{}).Build()
			ctx := context.Background()
			if tc.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			output, err := execute(deps, ctx, tc.input)
			require.Error(t, err)
			var failure *commandFailure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, tc.exit, failure.details.ExitCode)
			require.Equal(t, tc.timedOut, failure.details.TimedOut)
			require.Equal(t, tc.timedOut, errors.Is(err, context.DeadlineExceeded))
			require.Equal(t, tc.canceled, errors.Is(err, context.Canceled))
			require.False(t, jobdb.IsAppError(err), "diagnostics must not coerce native failures to application errors")
			require.False(t, output.Success)
			require.Len(t, failure.details.Diagnostics, 1)
			require.Empty(t, failure.details.DiagnosticErrors)
			for _, artifact := range deps.GetOutputArtifacts() {
				data, e := artifact.Bytes(context.Background())
				require.NoError(t, e)
				require.Equal(t, "_c2j/execution/execution.log", artifact.Name())
				require.Contains(t, string(data), "Working directory:")
				require.Contains(t, string(data), "Result:")
				if tc.stdout != "" {
					require.Contains(t, string(data), strings.TrimSpace(tc.stdout))
				}
				if tc.stderr != "" {
					require.Contains(t, string(data), strings.TrimSpace(tc.stderr))
				}

				ref := failure.details.Diagnostics[artifact.Name()]
				require.NoError(t, ref.Validate())
				key, ok := ref.StoredKey()
				require.True(t, ok)
				require.Equal(t, int64(17), key.TaskOrdinal)
				require.Equal(t, int64(len(data)), key.SizeBytes)
			}
		})
	}
}

func TestCommandDiagnosticsPreserveNativeTimeoutCause(t *testing.T) {
	native := jobdb.NewTimeoutError("task", time.Second, jobdb.TimeoutScopeTotal, nil, false)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(native)
	deps := ops.NewOpDependenciesBuilder().WithJobTool(diagnosticJobTool{}).Build()
	output, err := execute(deps, ctx, CommandExecutionInput{Run: "sleep 5"})
	require.True(t, output.TimedOut)
	require.ErrorIs(t, err, native)
	var timeout *jobdb.TimeoutError
	require.ErrorAs(t, err, &timeout)
	require.False(t, timeout.Payload.Retryable)
	require.Equal(t, jobdb.TimeoutScopeTotal, timeout.Payload.Scope)
	var failure *commandFailure
	require.ErrorAs(t, err, &failure)
	require.True(t, failure.details.TimedOut)
	require.Len(t, failure.details.Diagnostics, 1)
}

func TestCommandDiagnosticsDoNotChangeSuccessOrContinue(t *testing.T) {
	for _, input := range []CommandExecutionInput{
		{Run: "printf 'ok\n'"},
		{Run: "printf 'ok\n'; exit 7", ContinueOnError: true},
		{Run: "printf 'ok\n'; sleep 5", Timeout: "200ms", ContinueOnError: true},
	} {
		deps := ops.NewOpDependenciesBuilder().WithJobTool(diagnosticJobTool{}).Build()
		output, err := execute(deps, context.Background(), input)
		require.NoError(t, err)
		require.Equal(t, "ok", output.Stdout)
		require.Empty(t, deps.GetOutputArtifacts())
	}
}

func TestCommandDiagnosticCollectionFailureAndRedaction(t *testing.T) {
	deps := ops.NewOpDependenciesBuilder().WithJobTool(diagnosticJobTool{}).Build()
	redactor := secrets.NewRedactor()
	require.NoError(t, redactor.Register("private-credential"))
	ctx := secrets.WithRedactor(context.Background(), redactor)
	_, err := execute(deps, ctx, CommandExecutionInput{Run: "printf private-credential; printf private-credential >&2; exit 7"})
	require.EqualError(t, err, "command execution failed: exit status 7")
	for _, artifact := range deps.GetOutputArtifacts() {
		raw, e := artifact.Bytes(ctx)
		require.NoError(t, e)
		require.Contains(t, string(raw), "[REDACTED]")
		require.NotContains(t, string(raw), "private-credential")
	}
	_, err = execute(rejectingArtifacts{deps}, ctx, CommandExecutionInput{Run: "exit 7"})
	require.EqualError(t, err, "command execution failed: exit status 7")
	var failure *commandFailure
	require.ErrorAs(t, err, &failure)
	require.Len(t, failure.details.DiagnosticErrors, 1)
	require.Empty(t, failure.details.Diagnostics)
}

func TestCommandPreviewsAndAutomaticLogs(t *testing.T) {
	for _, continued := range []bool{false, true} {
		deps := ops.NewOpDependenciesBuilder().WithJobTool(diagnosticJobTool{}).WithGitContext(ops.GitExecutionContext{NodePath: "checks/large"}).Build()
		script := `head -c 20000 /dev/zero | tr '\000' x; head -c 20000 /dev/zero | tr '\000' y >&2`
		if continued {
			script += "; exit 7"
		}
		out, err := execute(deps, context.Background(), CommandExecutionInput{Run: script, ContinueOnError: continued})
		require.NoError(t, err)
		require.Equal(t, !continued, out.Success)
		require.True(t, out.StdoutTruncated)
		require.True(t, out.StderrTruncated)
		for _, preview := range []string{out.Stdout, out.Stderr} {
			require.LessOrEqual(t, len(preview), executiondiag.PreviewLimit)
			require.True(t, strings.HasSuffix(preview, "[Output truncated. See execution log: _c2j/execution/large.log]"))
		}
		require.Len(t, out.Diagnostics, 1)
		require.Len(t, deps.GetOutputArtifacts(), 1)
		raw, e := deps.GetOutputArtifacts()[0].Bytes(context.Background())
		require.NoError(t, e)
		counts := [2]int{}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "[stdout]") {
				counts[0] += strings.Count(line, "x")
			}
			if strings.Contains(line, "[stderr]") {
				counts[1] += strings.Count(line, "y")
			}
		}
		require.Equal(t, [2]int{20000, 20000}, counts)
		encoded, e := json.Marshal(out)
		require.NoError(t, e)
		require.Less(t, len(encoded), executiondiag.OutputLimit)
	}
}
func TestCommandAlwaysAndUnavailableLog(t *testing.T) {
	deps := ops.NewOpDependenciesBuilder().WithJobTool(diagnosticJobTool{}).Build()
	out, err := execute(deps, context.Background(), CommandExecutionInput{Run: "printf ok", CaptureDiagnostics: "always"})
	require.NoError(t, err)
	require.Equal(t, "ok", out.Stdout)
	require.Len(t, out.Diagnostics, 1)
	out, err = execute(rejectingArtifacts{deps}, context.Background(), CommandExecutionInput{Run: `head -c 20000 /dev/zero | tr '\000' x`})
	require.NoError(t, err)
	require.True(t, out.Success)
	require.True(t, out.StdoutTruncated)
	require.Contains(t, out.Stdout, "Execution log unavailable")
	require.NotEmpty(t, out.DiagnosticErrors)
}

func TestCommandRejectedInvocationHasNoExecutionClaim(t *testing.T) {
	deps := ops.NewOpDependenciesBuilder().WithJobTool(diagnosticJobTool{}).Build()
	_, err := execute(deps, context.Background(), CommandExecutionInput{Run: "echo should-not-run", Timeout: "invalid"})
	require.ErrorContains(t, err, "invalid timeout format")
	require.Len(t, deps.GetOutputArtifacts(), 1)
	raw, e := deps.GetOutputArtifacts()[0].Bytes(context.Background())
	require.NoError(t, e)
	require.Contains(t, string(raw), "Result: rejected before launch")
	require.Contains(t, string(raw), "Started: not launched")
	require.NotContains(t, string(raw), "[stdout]")
	require.NotContains(t, string(raw), "Duration:")
}
