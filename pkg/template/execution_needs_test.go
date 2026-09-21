package template

import (
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExecutionNeedsTemplatesAndScopeRestoration(t *testing.T) {
	root := newRecipeCtx(t, nil)
	parent := newSequenceCtx(t, root, "parent", map[string]interface{}{"memory": "16Gi", "cpu": 4})
	require.NoError(t, parent.ResolveExecutionNeeds(&recipe.ExecutionNeeds{
		Resources: recipe.ExecutionNeedResources{CPU: "${{ inputs.cpu }}", Memory: "${{ inputs.memory }}", EphemeralStorage: "${{ '10Gi' }}"},
		Platform:  "${{ 'linux/amd64' }}", Image: "${{ 'example.com/runner:a' }}",
	}))
	addOpOutput(t, parent, "probe", map[string]interface{}{"memory": "8Gi"})
	child, err := parent.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "child"}, "", nil)
	require.NoError(t, err)
	require.NoError(t, child.ResolveExecutionNeeds(&recipe.ExecutionNeeds{
		Resources: recipe.ExecutionNeedResources{Memory: "${{ sequence.probe.outputs.memory }}"},
		Platform:  "linux/arm64", Image: "example.com/runner:b",
	}))
	require.Equal(t, "4", *child.ExecutionNeeds.Resources.CPU)
	require.Equal(t, "8Gi", *child.ExecutionNeeds.Resources.Memory)
	require.Equal(t, "10Gi", *child.ExecutionNeeds.Resources.EphemeralStorage)
	require.Equal(t, "linux/arm64", *child.ExecutionNeeds.Platform)
	sibling, err := parent.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "sibling"}, "", nil)
	require.NoError(t, err)
	require.Equal(t, "16Gi", *sibling.ExecutionNeeds.Resources.Memory)
	require.Equal(t, "linux/amd64", *sibling.ExecutionNeeds.Platform)
	require.True(t, root.ExecutionNeeds.Empty())
	separate := newRecipeCtx(t, nil)
	require.True(t, separate.ExecutionNeeds.Empty())
}

func TestExecutionNeedsInvalidValuesIdentifyNodeAndProperty(t *testing.T) {
	for _, v := range []any{"${{ inputs.missing }}", "0Gi", true, 16, ""} {
		ctx := newSequenceCtx(t, newRecipeCtx(t, nil), "bad-node", map[string]interface{}{})
		err := ctx.ResolveExecutionNeeds(&recipe.ExecutionNeeds{Resources: recipe.ExecutionNeedResources{Memory: v}})
		require.Error(t, err)
		require.Contains(t, err.Error(), "bad-node")
		require.Contains(t, err.Error(), "memory")
	}
}
