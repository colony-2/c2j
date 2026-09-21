package recipe

import (
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"testing"
)

func TestExecutionNeedsParsingRoundTripAndSchema(t *testing.T) {
	registerTestOp()
	for _, body := range []string{"op: echo\ninputs: {message: hi}", "sequence: []", "state:\n  initial: done\n  states:\n    done:\n      sequence: []", "child_group:\n  children: []"} {
		r, err := LoadRecipeFromString([]byte("id: needs\nexecution_needs:\n  resources:\n    cpu: 4\n    memory: '${{ inputs.memory }}'\n" + body))
		require.NoError(t, err)
		require.True(t, HasExecutionNeeds(*r))
		raw, err := yaml.Marshal(r)
		require.NoError(t, err)
		again, err := LoadRecipeFromString(raw)
		require.NoError(t, err)
		require.Equal(t, r.GetMetadata().ExecutionNeeds, again.GetMetadata().ExecutionNeeds)
	}
	for _, field := range []string{"memroy: 4Gi", "resources: []", "resources: {memroy: 4Gi}"} {
		_, err := LoadRecipeFromString([]byte("id: bad\nexecution_needs:\n  " + field + "\nsequence: []"))
		require.Error(t, err)
	}
	schema, err := GenerateSchemaString()
	require.NoError(t, err)
	require.Contains(t, schema, `"execution_needs"`)
}
