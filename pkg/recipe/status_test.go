package recipe

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRecipeStatusAuthoring(t *testing.T) {
	registerTestOp()
	for _, body := range []string{
		"op: echo\ninputs: {message: hi}",
		"sequence: []",
		"state: {initial: done, states: {done: {sequence: [], status: null}}}",
		"child_group: {children: []}",
		"parallel: {branches: [{id: first, sequence: []}, {id: second, sequence: []}]}",
	} {
		for _, declaration := range []struct{ source, encoded string }{
			{},
			{"status: null\n", "null"},
			{"status: Ready\n", `"Ready"`},
			{"status: false\n", "false"},
			{"status: 0\n", "0"},
			{"status: []\n", "[]"},
			{"status: {}\n", "{}"},
			{"status: {checks: [9007199254740993, true, null, ['${{ inputs.count }}']]}\n", `{"checks":[9007199254740993,true,null,["${{ inputs.count }}"]]}`},
		} {
			source := "id: readiness\nversion: \"1.0\"\n" + declaration.source + body + "\n"
			recipe, err := LoadRecipeFromString([]byte(source))
			require.NoError(t, err, source)
			raw, err := yaml.Marshal(recipe)
			require.NoError(t, err)
			again, err := LoadInternalRecipeFromString(raw)
			require.NoError(t, err)
			for _, parsed := range []*Recipe{recipe, again} {
				status := parsed.GetMetadata().Status
				if declaration.encoded == "" {
					require.Nil(t, status)
				} else {
					require.NotNil(t, status)
					actual, err := json.Marshal(status)
					require.NoError(t, err)
					require.Equal(t, declaration.encoded, string(actual))
				}
				if state, ok := parsed.RecipeImpl.(*RecipeState); ok {
					require.Equal(t, &StatusTemplate{}, state.States.States["done"].GetMetadata().Status)
				}
			}
		}
	}
	schema, err := GenerateSchemaString()
	require.NoError(t, err)
	require.Contains(t, schema, `"status": true`)
	require.NoError(t, Validate("version: \"1.0\"\nop: echo\ninputs: {message: hi}\nstatus: {checks: [null, false]}\n"))
}

func TestRecipeStatusSharedOverride(t *testing.T) {
	const source = `id: readiness
defs:
  leaf:
    status: definition
    sequence:
      - status: descendant
        sequence: []
  alias:
    shared: leaf
    status: alias
sequence:
  - shared: leaf
    status: null
  - shared: alias
  - shared: leaf
  - shared: alias
    status: callsite
  - include: library.yaml
    status: '${{ {"phase": "ready"} }}'
`
	parsed, err := LoadRecipeFromString([]byte(source))
	require.NoError(t, err)
	raw, err := yaml.Marshal(parsed)
	require.NoError(t, err)
	resolved, err := LoadInternalRecipeFromString(raw)
	require.NoError(t, err)
	for _, candidate := range []*Recipe{parsed, resolved} {
		nodes := candidate.RecipeImpl.(*RecipeSequence).Sequence
		for index, expected := range []any{nil, "alias", "definition", "callsite"} {
			seq := nodes[index].NodeImpl.(*NodeSequence)
			require.Equal(t, &StatusTemplate{Value: expected}, seq.Status)
			require.Equal(t, &StatusTemplate{Value: "descendant"}, seq.Sequence[0].GetMetadata().Status)
		}
		require.Equal(t, &StatusTemplate{Value: `${{ {"phase": "ready"} }}`}, nodes[4].GetMetadata().Status)
	}
}
