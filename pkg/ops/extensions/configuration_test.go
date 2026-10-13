package extensions

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestConfigurationFallbacksValidatedBeforeExtensionExecution(t *testing.T) {
	for _, tc := range []struct {
		name, expr      string
		nullable, valid bool
	}{
		{"missing nullable", `context.config.setting`, true, true},
		{"null nullable", `context.config.?setting.orValue(null)`, true, true},
		{"missing string", `context.config.setting`, false, false},
		{"null string", `context.config.?setting.orValue(null)`, false, false},
		{"fallback string", `context.config.?setting.orValue("stable")`, false, true},
		{"wrong fallback type", `context.config.?setting.orValue(false)`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var typ any = "string"
			if tc.nullable {
				typ = []any{"string", "null"}
			}
			schema := map[string]any{"type": "object", "required": []any{"setting"}, "properties": map[string]any{
				"setting": map[string]any{"type": typ, "default": "schema-default"},
			}}
			resolved, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
			require.NoError(t, err)
			ref, ok := configuration.Expression(tc.expr)
			require.True(t, ok)
			input := map[string]any{"setting": ref}
			_, err = resolved.ApplyInvocationDefaults(input)
			require.NoError(t, err)
			require.Equal(t, ref, input["setting"], "ordinary schema defaults don't replace explicit references")
			out, _, err := configuration.MaterializeInput(context.Background(), nil, input, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
				return jobdb.ConfigurationSnapshot{}, nil
			})
			require.NoError(t, err)
			values := out.(map[string]any)
			require.Contains(t, values, "setting", "null is a value, not omission")
			err = resolved.ValidateInvocationInputs(values)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
