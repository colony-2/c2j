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
		Base:      "${{ 'ubuntu*amd64' }}",
	}))
	addOpOutput(t, parent, "probe", map[string]interface{}{"memory": "8Gi"})
	child, err := parent.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "child"}, "", nil)
	require.NoError(t, err)
	require.NoError(t, child.ResolveExecutionNeeds(&recipe.ExecutionNeeds{
		Resources: recipe.ExecutionNeedResources{Memory: "${{ sequence.probe.outputs.memory }}"},
		Base:      "macos*",
	}))
	require.Equal(t, "4", *child.ExecutionNeeds.Resources.CPU)
	require.Equal(t, "8Gi", *child.ExecutionNeeds.Resources.Memory)
	require.Equal(t, "10Gi", *child.ExecutionNeeds.Resources.EphemeralStorage)
	require.Equal(t, "macos*", *child.ExecutionNeeds.Base)
	sibling, err := parent.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "sibling"}, "", nil)
	require.NoError(t, err)
	require.Equal(t, "16Gi", *sibling.ExecutionNeeds.Resources.Memory)
	require.Equal(t, "ubuntu*amd64", *sibling.ExecutionNeeds.Base)
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

func TestEmptyCapabilitiesRetainOuterDeclaration(t *testing.T) {
	root := newRecipeCtx(t, nil)
	parent := newSequenceCtx(t, root, "parent", map[string]interface{}{})
	require.NoError(t, parent.ResolveExecutionNeeds(&recipe.ExecutionNeeds{Capabilities: []string{"outer"}}))
	child, err := parent.NewChildContext(ScopeOp, recipe.NodeMetadata{ID: "child"}, "", nil)
	require.NoError(t, err)
	require.NoError(t, child.ResolveExecutionNeeds(&recipe.ExecutionNeeds{Capabilities: []string{}}))
	require.True(t, child.ExecutionNeeds.CapabilitiesDeclared)
	require.Equal(t, []string{"outer"}, child.ExecutionNeeds.Capabilities)
	require.False(t, root.ExecutionNeeds.CapabilitiesDeclared)
}
