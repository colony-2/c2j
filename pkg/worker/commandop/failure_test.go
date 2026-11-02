package commandop

import (
	"context"
	"errors"
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
			require.Len(t, failure.details.Diagnostics, 2)
			require.Empty(t, failure.details.DiagnosticErrors)
			for _, artifact := range deps.GetOutputArtifacts() {
				data, e := artifact.Bytes(context.Background())
				require.NoError(t, e)
				expected := tc.stdout
				if artifact.Name() == StderrArtifactName {
					expected = tc.stderr
				}
				require.Equal(t, expected, string(data))
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
	require.Len(t, failure.details.Diagnostics, 2)
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
		require.Equal(t, "[REDACTED]", string(raw))
	}
	_, err = execute(rejectingArtifacts{deps}, ctx, CommandExecutionInput{Run: "exit 7"})
	require.EqualError(t, err, "command execution failed: exit status 7")
	var failure *commandFailure
	require.ErrorAs(t, err, &failure)
	require.Len(t, failure.details.DiagnosticErrors, 2)
	require.Empty(t, failure.details.Diagnostics)
}
