package ops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestPreparedConfigurationDefaultDurabilityAndExecution(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "echo"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "echo/op.yaml"), []byte(`name: echo
shell: sh
run: cat
input_schema:
 type: object
 properties:
  value: {type: string, default: '${{ context.config.?value.orValue(null) }}'}
output_schema: {type: object}
`), 0600))
	for _, tc := range []struct {
		name    string
		input   map[string]any
		value   string
		invalid bool
		want    map[string]any
	}{
		{"missing", map[string]any{}, "", false, map[string]any{}},
		{"null setting", map[string]any{}, "null", false, map[string]any{}},
		{"configured", map[string]any{}, `"live"`, false, map[string]any{"value": "live"}},
		{"explicit null", map[string]any{"value": nil}, "", true, nil},
		{"override", map[string]any{"value": ""}, "null", false, map[string]any{"value": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			inv := ActivityInvocationRequest{Input: map[string]any{"selector": "./echo", "inputs": tc.input}, Preparation: PreparationRequest{Extension: &ExtensionResolutionRequest{Selector: "./echo", Options: extops.ResolveOptions{BaseDir: root}}}}
			req, err := ResolveInvocation(ctx, &inv)
			require.NoError(t, err)
			// Both the invocation and prepared manifest cross the durable JSON boundary.
			raw, err := json.Marshal(inv)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &inv))
			raw, err = json.Marshal(req)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &req))
			req.Extension, err = extops.RestoreResolvedOp(req.Extension)
			require.NoError(t, err)
			reg := ActivityRegistration{Step: coreops.TaskStep{InputType: reflect.TypeFor[extops.ExecutionInput]()}}
			calls := 0
			_, err = materializeConfiguration(ctx, reg, &inv, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
				calls++
				values := map[string]json.RawMessage{}
				if tc.value != "" {
					values["value"] = json.RawMessage(tc.value)
				}
				return jobdb.ConfigurationSnapshot{Values: values}, nil
			})
			require.NoError(t, err)
			if tc.name == "override" || tc.name == "explicit null" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
			err = InstallInvocation(ctx, &inv, req, func(context.Context, ToolSetupRequest) ToolSetupResult {
				return ToolSetupResult{Extension: req.Extension}
			})
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			deps := coreops.NewOpDependenciesBuilder().WithWorktreePath(root).Build()
			result, err := extops.GetExecutionOp().TaskChain()[0].Invoke(deps, extops.WithPreparedOp(ctx, req.Extension), inv.Input)
			require.NoError(t, err)
			require.Equal(t, tc.want, result)
		})
	}
}
