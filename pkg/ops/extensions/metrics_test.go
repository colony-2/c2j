package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/metrics"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/shellcmd"
	"github.com/stretchr/testify/require"
)

const usageReport = `{"version":1,"mutations":[{"name":"llm_tokens_total","kind":"counter","number_type":"int64","unit":"{token}","labels":{"model":"returned-model"},"op":"add","value":9007199254740993}]}`

func TestExtensionMetricsOnSuccessFailureAndSchemaFailure(t *testing.T) {
	cases := []struct {
		name, output string
		exit         int
		status       string
		failure      bool
	}{
		{"success", `{"output":{"answer":"ok"},"metrics":` + usageReport + `}`, 0, "valid", false},
		{"exit", `{"output":null,"metrics":` + usageReport + `,"objects":{"bad":{}},"artifact_refs":{"bad":{}}}`, 7, "valid", true},
		{"validation", `{"output":{"answer":4},"metrics":` + usageReport + `}`, 0, "valid", true},
		{"invalid metrics", `{"output":{"answer":"ok"},"metrics":{"version":1,"identity":"forged","mutations":[]}}`, 0, "invalid", false},
		{"plain business", `{"answer":"ok","metrics":{"business":4}}`, 0, "absent", false},
		{"empty failure", "", 7, "unavailable", true},
		{"truncated JSON", `{"output":null,"metrics":`, 7, "unavailable", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "op")
			require.NoError(t, os.Mkdir(dir, 0700))
			body := "name: metric-test\nshell: sh\nrun: |\n  " + shellcmd.LiteralLine(tc.output) + "\n  exit " + string(rune('0'+tc.exit)) + "\ninput_schema: {type: object}\noutput_schema: {type: object, required: [answer], properties: {answer: {type: string}}}\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "op.yaml"), []byte(body), 0600))
			collector := metrics.NewCollector(nil)
			ctx := metrics.WithCollector(context.Background(), collector)
			deps := coreops.NewOpDependenciesBuilder().WithWorktreePath(root).Build()
			out, err := executeExtension(deps, ctx, ExecutionInput{Selector: "./op"})
			if tc.failure {
				require.Error(t, err)
				require.Nil(t, out)
			} else {
				require.NoError(t, err)
			}
			if tc.exit != 0 {
				var exit *exec.ExitError
				require.True(t, errors.As(err, &exit))
				require.Equal(t, tc.exit, exit.ExitCode())
			}
			channel := collector.Snapshot()
			require.Equal(t, tc.status, channel.Status)
			if tc.status == "valid" {
				require.Equal(t, int64(9007199254740993), channel.Report.Mutations[0].Value)
			}
			for _, artifact := range deps.GetOutputArtifacts() {
				require.NotEqual(t, "bad", artifact.Name())
			}
			if tc.name == "plain business" {
				require.Contains(t, out, "metrics")
			}
		})
	}
}
func TestMetricSecretsAndTruncationAreNotRetained(t *testing.T) {
	redactor := secrets.NewRedactor()
	defer redactor.Close()
	require.NoError(t, redactor.Register("top-secret-material"))
	c := metrics.NewCollector(redactor.CheckOutput)
	ctx := metrics.WithCollector(context.Background(), c)
	captureMetricReport(ctx, []byte(`{"output":null,"metrics":{"version":1,"mutations":[{"name":"tokens","kind":"counter","number_type":"int64","op":"add","value":1,"labels":{"model":"top-secret-material"}}]}}`), true, false)
	require.Equal(t, "invalid", c.Snapshot().Status)
	require.Nil(t, c.Snapshot().Report)
	require.NotContains(t, c.Snapshot().Diagnostic, "top-secret-material")
	c = metrics.NewCollector(nil)
	captureMetricReport(metrics.WithCollector(context.Background(), c), []byte(`{"output":{},"metrics":`+usageReport+`}`), false, true)
	require.Equal(t, "unavailable", c.Snapshot().Status)
}

// Output is a common business field, not an envelope discriminator by itself.
func TestPlainExtensionOutputFieldsRemainBusinessData(t *testing.T) {
	for _, value := range []string{`"text"`, `42`, `true`, `[1,2]`, `null`} {
		t.Run(value, func(t *testing.T) {
			root := t.TempDir()
			wire := `{"answer":"ok","output":` + value + `,"metrics":` + usageReport + `,"objects":{"ordinary":"data"}}`
			spec := "name: business\nshell: sh\nrun: |\n  " + shellcmd.LiteralLine(wire) + "\ninput_schema: {type: object}\noutput_schema: {type: object, required: [answer, output, metrics, objects]}\n"
			require.NoError(t, os.WriteFile(filepath.Join(root, "op.yaml"), []byte(spec), 0600))
			collector := metrics.NewCollector(nil)
			out, err := executeExtension(coreops.NewOpDependenciesBuilder().WithWorktreePath(root).Build(), metrics.WithCollector(context.Background(), collector), ExecutionInput{Selector: "./"})
			require.NoError(t, err)
			actual, err := json.Marshal(out)
			require.NoError(t, err)
			require.JSONEq(t, wire, string(actual))
			require.Equal(t, "absent", collector.Snapshot().Status)
		})
	}
}
