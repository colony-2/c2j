package recipe

import (
	"encoding/json"
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
	require.Contains(t, schema, `"base"`)
}

func TestCapabilitiesRecipeAndNodeParsing(t *testing.T) {
	for _, field := range []string{"execution", "execution_needs"} {
		for _, value := range []string{"[true]", "[123]", "[null]", "[Docker]", "docker", "[a..b]"} {
			_, err := LoadRecipeFromString([]byte("id: bad\n" + field + ": {capabilities: " + value + "}\nsequence: []\n"))
			require.Error(t, err, field+value)
		}
		r, err := LoadRecipeFromString([]byte("id: good\n" + field + ": {capabilities: [unknown.feature, vendor/device]}\nsequence: []\n"))
		require.NoError(t, err)
		if field == "execution_needs" {
			require.True(t, HasExecutionConstraints(*r))
		}
		raw, err := yaml.Marshal(r)
		require.NoError(t, err)
		again, err := LoadRecipeFromString(raw)
		require.NoError(t, err)
		require.Equal(t, r.GetMetadata().Execution, again.GetMetadata().Execution)
		require.Equal(t, r.GetMetadata().ExecutionNeeds, again.GetMetadata().ExecutionNeeds)
	}
	schema, err := GenerateSchemaString()
	require.NoError(t, err)
	require.Contains(t, schema, `"capabilities"`)
}

func TestExplicitEmptyCapabilitiesSurviveRecipeEncoding(t *testing.T) {
	registerTestOp()
	for _, source := range []string{"id: empty\nexecution_needs: {capabilities: []}\nop: echo\n", "id: empty\nexecution: {capabilities: []}\nop: echo\n"} {
		r, err := LoadRecipeFromString([]byte(source))
		require.NoError(t, err)
		raw, err := yaml.Marshal(r)
		require.NoError(t, err)
		again, err := LoadRecipeFromString(raw)
		require.NoError(t, err)
		if n := again.GetMetadata().ExecutionNeeds; n != nil {
			require.True(t, n.CapabilitiesDeclared)
		} else {
			require.True(t, again.GetMetadata().Execution.CapabilitiesDeclared)
		}
	}
}

func TestProgrammaticEmptyCapabilitiesJSONPresence(t *testing.T) {
	raw, err := json.Marshal(ExecutionNeeds{Capabilities: []string{}})
	require.NoError(t, err)
	var n ExecutionNeeds
	require.NoError(t, json.Unmarshal(raw, &n))
	require.True(t, n.CapabilitiesDeclared)
}
