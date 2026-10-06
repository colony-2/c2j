package extensions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestExtensionSandboxFieldUsesOrdinarySchemaValidation(t *testing.T) {
	for _, strict := range []bool{true, false} {
		t.Run(fmt.Sprintf("strict=%v", strict), func(t *testing.T) {
			root := t.TempDir()
			opDir := filepath.Join(root, "echo")
			require.NoError(t, os.MkdirAll(opDir, 0700))
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".shai"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".shai", "config.yaml"), []byte("[invalid: yaml"), 0600))
			manifest := fmt.Sprintf(`name: echo
shell: sh
run: touch marker; cat
input_schema:
  type: object
  additionalProperties: %v
output_schema:
  type: object
`, !strict)
			require.NoError(t, os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(manifest), 0600))
			deps := ops.NewOpDependenciesBuilder().WithWorktreePath(root).Build()
			for _, value := range []any{map[string]any{"type": "shai"}, map[string]any{"type": "none"}, map[string]any{}, nil, false, "${{ inputs.mode }}"} {
				payload := map[string]any{"sandbox": value}
				result, err := GetExecutionOp().TaskChain()[0].Invoke(deps, context.Background(), map[string]any{"selector": "./echo", "inputs": payload})
				if strict {
					require.ErrorContains(t, err, "extension input validation failed")
					_, statErr := os.Stat(filepath.Join(opDir, "marker"))
					require.True(t, os.IsNotExist(statErr))
				} else {
					require.NoError(t, err)
					// No reserved sandbox field: permissive extensions receive ordinary data.
					require.Equal(t, payload, result)
				}
			}
		})
	}
}
