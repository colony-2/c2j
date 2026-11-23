package recipe

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestOptionalInputsParsing(t *testing.T) {
	tests := []struct {
		name, yaml, wantErr string
	}{
		{"root", "op: ./compress\noptional_inputs: {level: '${{ inputs.?level }}'}\n", ""},
		{"nested", "sequence:\n  - op: ./compress\n    optional_inputs: {level: '${{ inputs.?level }}'}\n", ""},
		{"overlap", "op: ./compress\ninputs: {level: 0}\noptional_inputs: {level: '${{ inputs.?level }}'}\n", "both inputs and optional_inputs"},
		{"null block", "op: ./compress\noptional_inputs: null\n", "optional_inputs must be a mapping"},
		{"built-in", "op: command_execution\noptional_inputs: {}\n", "only supported on direct extension ops"},
		{"non-op", "sequence:\n  - include: ./other.yaml\n    optional_inputs: {}\n", "only supported on direct extension ops"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, err := LoadRecipeFromString([]byte("id: root\nversion: '1'\n" + tc.yaml))
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			raw, err := yaml.Marshal(rec)
			require.NoError(t, err)
			require.Contains(t, string(raw), "optional_inputs:")
			require.Contains(t, string(raw), "${{ inputs.?level }}")
		})
	}
}
