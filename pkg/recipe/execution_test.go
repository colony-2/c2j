package recipe

import (
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
)

func TestExecutionDeclarationParsingAndSchema(t *testing.T) {
	registerTestOp()
	for _, body := range []string{"op: echo\ninputs: {message: hi}", "sequence: []", "state:\n  initial: done\n  states:\n    done:\n      sequence: []", "child_group:\n  children: []"} {
		raw := "id: test\nexecution:\n  resources:\n    memory: 1024Mi\n" + body + "\n"
		r, err := LoadRecipeFromString([]byte(raw))
		require.NoError(t, err, body)
		require.Equal(t, "1Gi", *r.GetMetdata().Execution.Resources.Memory)
		encoded, err := yaml.Marshal(r)
		require.NoError(t, err)
		again, err := LoadRecipeFromReader(strings.NewReader(string(encoded)))
		require.NoError(t, err)
		require.Equal(t, r.GetMetdata().Execution, again.GetMetdata().Execution)
	}
	_, err := LoadRecipeFromString([]byte("id: invalid\nexecution:\n  resources:\n    memory: 0Gi\nsequence: []\n"))
	require.Error(t, err)
	schema, err := GenerateSchemaString()
	require.NoError(t, err)
	require.Contains(t, schema, `"execution"`)
}
