package compiler

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRecipeStatusTemplateRendering(t *testing.T) {
	job, git := GenerateTestContext()
	resolver, err := template.NewRecipeResolutionContext(&git, map[string]any{"count": int64(9007199254740993)}, job)
	require.NoError(t, err)
	for _, declaration := range []string{
		`{count: '${{ inputs.count }}', phase: [true, null]}`,
		`'${{ {"count": inputs.count, "phase": [true, null]} }}'`,
	} {
		parsed, err := recipe.LoadRecipeFromString([]byte("id: status\nsequence: []\nstatus: " + declaration + "\n"))
		require.NoError(t, err)
		value, err := resolver.ResolveValueWithMode(parsed.GetMetadata().Status.Value, template.ModeInterpolation)
		require.NoError(t, err)
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		require.Equal(t, `{"count":9007199254740993,"phase":[true,null]}`, string(encoded))
	}
}

func TestRecipeStatusIncludeDeclaration(t *testing.T) {
	callee, err := recipe.LoadRecipeFromString([]byte(`id: library
status: {phase: '${{ vars.phase }}'}
sequence:
  - status: descendant
    sequence: []
`))
	require.NoError(t, err)
	for _, example := range []struct {
		name, declaration, expected string
	}{
		{"inherit", "", `{"phase":"${{ vars.phase }}"}`},
		{"replace", "status: called\n", `"called"`},
		{"clear", "status: null\n", `null`},
	} {
		t.Run(example.name, func(t *testing.T) {
			var include recipe.Node
			require.NoError(t, yaml.Unmarshal([]byte("include: ./library.yaml\n"+example.declaration), &include))
			wrapper, err := inlineWrapperNode(include.NodeImpl.(*recipe.NodeInclude), *callee, loadedInlineRecipe{}, "caller/library", "")
			require.NoError(t, err)
			raw, err := yaml.Marshal(wrapper)
			require.NoError(t, err)
			var restored recipe.Node
			require.NoError(t, yaml.Unmarshal(raw, &restored))
			for _, node := range []recipe.Node{wrapper, restored} {
				root := node.NodeImpl.(*recipe.NodeSequence).Sequence[0].NodeImpl.(*recipe.NodeSequence)
				declaration := node.GetMetadata().Status
				if example.name == "inherit" {
					require.Nil(t, declaration)
					declaration = root.Status
				}
				require.NotNil(t, declaration)
				status, err := json.Marshal(declaration)
				require.NoError(t, err)
				require.Equal(t, example.expected, string(status))
				require.Equal(t, &recipe.StatusTemplate{Value: "descendant"}, root.Sequence[0].GetMetadata().Status)
			}
		})
	}
}
