package compiler

import (
	"context"
	"fmt"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/stretchr/testify/require"
)

func TestCommandSandboxIsAnUnknownInput(t *testing.T) {
	for _, value := range []any{map[string]any{"type": "shai"}, map[string]any{"type": "none"}, map[string]any{}, nil, false, 123, "${{ inputs.mode }}", []any{"shai"}} {
		for _, validationMode := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/validation=%v", value, validationMode), func(t *testing.T) {
				err := validateOpInputType(reflect.TypeOf(commandop.CommandExecutionInput{}), map[string]any{"run": "echo hello", "sandbox": value}, validationMode)
				require.ErrorContains(t, err, "invalid keys: sandbox")
			})
		}
	}
}

func TestUnknownCommandInputInExpandedAndPersistedRecipes(t *testing.T) {
	withRegisteredOps(t, commandop.GetOp())
	root := t.TempDir()
	childPath := filepath.Join(root, "child.yaml")
	command := `id: old-command
op: command_execution
inputs:
  run: echo must-not-run
  sandbox: "${{ {'type': 'none'} }}"
  continue_on_error: true
`
	require.NoError(t, os.WriteFile(childPath, []byte("id: child\nsequence:\n  - id: old-command\n    op: command_execution\n    inputs: {run: echo must-not-run, sandbox: null}\noutputs: {}\n"), 0600))
	cases := map[string]string{
		"direct": command,
		"shared": `id: shared-command
defs:
  old:
    op: command_execution
    inputs: {run: echo must-not-run, sandbox: null, continue_on_error: true}
sequence:
  - id: old-command
    shared: old
`,
		"include": `id: included-command
sequence:
  - id: old-command
    include: ./child.yaml
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			rec, err := recipe.LoadRecipeFromString([]byte(source))
			require.NoError(t, err)
			expanded, err := ResolveInlineRecipes(context.Background(), *rec, InlineResolutionOptions{RootFile: filepath.Join(root, "root.yaml")})
			require.NoError(t, err)
			snapshot, err := yaml.Marshal(&expanded.Recipe)
			require.NoError(t, err)
			require.Contains(t, string(snapshot), "sandbox:")
			persisted, err := recipe.LoadInternalRecipeFromString(snapshot)
			require.NoError(t, err)
			jobCtx, gitCtx := GenerateTestContext()
			counter := &countingJobContext{jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "removed-input"}}
			_, _, err = ExecuteRecipe(newWorkflowContext(counter), *persisted, nil, jobCtx, gitCtx)
			require.ErrorContains(t, err, "invalid keys: sandbox")
			require.Zero(t, counter.calls)
		})
	}
}
