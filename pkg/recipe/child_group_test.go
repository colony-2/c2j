package recipe

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestChildGroupNodeUnmarshal(t *testing.T) {
	raw := `
id: parent
version: "1.0"
sequence:
  - id: children
    child_group:
      mode: run_and_get_result
      children:
        - key: first
          recipe: child-simple
          inputs:
            value: one
`
	var rec Recipe
	require.NoError(t, yaml.Unmarshal([]byte(raw), &rec))

	root, ok := rec.RecipeImpl.(*RecipeSequence)
	require.True(t, ok)
	require.Len(t, root.Sequence, 1)
	group, ok := root.Sequence[0].NodeImpl.(*NodeChildGroup)
	require.True(t, ok)
	require.Equal(t, "children", group.ID)
	require.Equal(t, "run_and_get_result", group.ChildGroup.Mode)
	require.Len(t, group.ChildGroup.Children, 1)
}

func TestChildGroupIncludedInGeneratedSchema(t *testing.T) {
	schema, err := GenerateSchemaString()
	require.NoError(t, err)
	require.True(t, strings.Contains(schema, "child_group"))
}
