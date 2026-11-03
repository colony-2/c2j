package extensions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/executiondiag"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type diagnosticsJobTool struct{ ops.JobTool }

func (diagnosticsJobTool) GetJobKey() jobdb.JobKey { return jobdb.JobKey{JobId: "extension-job"} }
func (diagnosticsJobTool) TaskOrdinal() int64      { return 19 }

func diagnosticExtension(t *testing.T, run, schema string) ops.OpDependencies {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "check")
	require.NoError(t, os.MkdirAll(dir, 0755))
	manifest := fmt.Sprintf("name: check\nshell: sh\nrun: %q\ninput_schema: {type: object}\noutput_schema: %s\n", run, schema)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "op.yaml"), []byte(manifest), 0600))
	return ops.NewOpDependenciesBuilder().WithWorktreePath(root).WithJobTool(diagnosticsJobTool{}).WithGitContext(ops.GitExecutionContext{NodePath: "steps/check"}).Build()
}
func readDiagnostic(t *testing.T, deps ops.OpDependencies) string {
	t.Helper()
	require.Len(t, deps.GetOutputArtifacts(), 1)
	artifact := deps.GetOutputArtifacts()[0]
	require.Equal(t, "_c2j/execution/check.log", artifact.Name())
	raw, err := artifact.Bytes(context.Background())
	require.NoError(t, err)
	return string(raw)
}
func TestExtensionAutomaticDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, run, schema, wantError string }{
		{"success", `printf '{"value":"ok"}'; printf 'notice\n' >&2`, "{type: object, additionalProperties: false, properties: {value: {type: string}}}", ""},
		{"process failure", `printf 'broken\n' >&2; exit 7`, "{type: object}", "exit status 7"},
		{"invalid JSON", `printf nope`, "{type: object}", "invalid JSON"},
		{"schema failure", `printf '{"value":42}'`, "{type: object, properties: {value: {type: string}}}", "schema validation"},
		{"oversized result", `printf '{"value":"'; head -c 200000 /dev/zero | tr '\000' x; printf '"}'`, "{type: object}", "output limit exceeded"},
		{"large malformed output", `head -c 200000 /dev/zero | tr '\000' x`, "{type: object}", "invalid JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := diagnosticExtension(t, tc.run, tc.schema)
			out, err := executeExtension(deps, context.Background(), ExecutionInput{Selector: "./check"})
			if tc.wantError == "" {
				require.NoError(t, err)
				require.Equal(t, map[string]any{"value": "ok"}, out)
			} else {
				require.ErrorContains(t, err, tc.wantError)
				require.Less(t, len(err.Error()), 2300)
				var failure *executiondiag.Failure
				require.ErrorAs(t, err, &failure)
				require.Len(t, failure.Details.Diagnostics, 1)
				require.Equal(t, 2, failure.Details.Version)
			}
			log := readDiagnostic(t, deps)
			require.Contains(t, log, "Extension: ./check")
			if tc.name != "process failure" {
				require.Contains(t, log, "Result: exited with code 0")
			}
			if tc.wantError != "" {
				require.Contains(t, log, "Operation result: failed:")
			} else {
				require.Contains(t, log, "Operation result: completed")
			}
		})
	}
}
func TestExtensionJSONSurvivesLargeDiagnostics(t *testing.T) {
	deps := diagnosticExtension(t, `head -c 5000000 /dev/zero | tr '\000' x >&2; printf '{"value":"'; head -c 20000 /dev/zero | tr '\000' y; printf '"}'`, "{type: object}")
	out, err := executeExtension(deps, context.Background(), ExecutionInput{Selector: "./check"})
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("y", 20000), out["value"])
	require.Contains(t, readDiagnostic(t, deps), "Capture truncated")
}
func TestExtensionCancellationAndRedaction(t *testing.T) {
	deps := diagnosticExtension(t, `printf private-; sleep .03; printf credential; printf private-credential >&2; sleep 5`, "{type: object}")
	redactor := secrets.NewRedactor()
	require.NoError(t, redactor.Register("private-credential"))
	ctx, cancel := context.WithTimeout(secrets.WithRedactor(context.Background(), redactor), 150*time.Millisecond)
	defer cancel()
	_, err := executeExtension(deps, ctx, ExecutionInput{Selector: "./check"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var failure *executiondiag.Failure
	require.ErrorAs(t, err, &failure)
	require.True(t, failure.Details.TimedOut)
	require.NotContains(t, err.Error(), "private-credential")
	log := readDiagnostic(t, deps)
	require.Contains(t, log, "[REDACTED]")
	require.NotContains(t, log, "private-credential")
	require.Contains(t, log, "Result: timed out")
}

type rejectDiagnostic struct{ ops.OpDependencies }

func (rejectDiagnostic) AddOutputArtifact(jobdb.Artifact) error {
	return errors.New("storage unavailable")
}
func TestExtensionCollectionFailurePreservesOutcome(t *testing.T) {
	for _, run := range []string{`printf '{"value":"ok"}'`, `exit 7`} {
		deps := rejectDiagnostic{diagnosticExtension(t, run, "{type: object}")}
		out, err := executeExtension(deps, context.Background(), ExecutionInput{Selector: "./check"})
		if run == "exit 7" {
			var failure *executiondiag.Failure
			require.ErrorAs(t, err, &failure)
			require.NotEmpty(t, failure.Details.DiagnosticErrors)
			require.ErrorContains(t, err, "exit status 7")
		} else {
			require.NoError(t, err)
			require.Equal(t, "ok", out["value"])
		}
	}
}
