package recipetest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/execution"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/stretchr/testify/require"
)

func TestPnpmVerifyHook(t *testing.T) {
	withRegisteredCoreOps(t, commandop.GetOp())
	root, err := filepath.Abs("../../testdata/recipes/verify-hook")
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(root, "verify.test.json"))
	require.NoError(t, err)
	var suite struct {
		Cases []Case `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &suite))
	body, err := os.ReadFile("../../.c2/recipes/hooks/build/verify.yaml")
	require.NoError(t, err)
	rec, err := recipe.LoadRecipeFromString(body)
	require.NoError(t, err)
	require.NotNil(t, rec.GetMetadata().Execution)
	require.NotNil(t, rec.GetMetadata().Execution.Base)
	require.Equal(t, "ubuntu-26.04-arm64", *rec.GetMetadata().Execution.Base)
	for _, c := range suite.Cases {
		t.Run(c.ID, func(t *testing.T) {
			// The suite doubles the large commands. Synthetic allocation permits
			// reporting/control-flow checks without asserting real arm64 execution.
			result := RunCase(context.Background(), HarnessOptions{FixtureRoot: root, WorkRoot: t.TempDir()}, "verify-hook", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: string(body)}, c, ExecutionOptions{Timeout: "20s"})
			require.Equal(t, "passed", result.Status, result.FailureReason)
		})
	}
}

func TestRuntimeSyntheticAllocationAdmission(t *testing.T) {
	withRegisteredCoreOps(t, commandop.GetOp())
	target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: "id: fixed\nexecution: {base: ubuntu-26.04-amd64}\nop: command_execution\ninputs: {run: 'exit 99'}\noutputs: {value: '${{ outputs.value }}'}\n"}
	for _, base := range []string{"ubuntu-26.04-amd64", "ubuntu-26.04-arm64"} {
		t.Run(base, func(t *testing.T) {
			c := Case{ID: "allocation", Type: "integration_case", Runtime: &RuntimeCase{Allocation: &execution.Allocation{SchemaVersion: execution.SchemaVersion, Base: base}}, Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: "return", Outputs: map[string]any{"value": "mocked"}}}}}}
			result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "allocation", target, c, ExecutionOptions{Timeout: "20s"})
			if base == "ubuntu-26.04-amd64" {
				require.Equal(t, "passed", result.Status, result.FailureReason)
				require.Equal(t, "mocked", result.Outputs["value"])
				require.Equal(t, c.Runtime.Allocation, result.Runtime.FixtureAllocation)
			} else {
				require.Equal(t, "failed", result.Status)
				require.Empty(t, result.Diagnostics.Calls, "incompatible allocation must not invoke even the mock")
			}
		})
	}
	issues := validateRuntimeCase(HarnessOptions{}, Case{Runtime: &RuntimeCase{Allocation: &execution.Allocation{SchemaVersion: execution.SchemaVersion, Base: "ubuntu*"}}})
	require.NotEmpty(t, issues, "allocation facts must use a concrete base")
}
