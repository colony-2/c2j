package compiler

import (
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestNewRecipeWorker(t *testing.T) {
	node := &recipe.Node{
		NodeImpl: &recipe.NodeOp{
			NodeMetadata: recipe.NodeMetadata{
				ID:   "test-recipe",
				Desc: "Test recipe",
				Inputs: map[string]interface{}{
					"type":   "function",
					"param1": "test_value",
				},
			},
			OpData: recipe.OpData{
				Op: "test_activity",
			},
		},
	}

	testRecipe := &recipe.Recipe{
		RecipeImpl: &recipe.RecipeOp{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: node.NodeImpl.(*recipe.NodeOp).NodeMetadata,
			},
			OpData: node.NodeImpl.(*recipe.NodeOp).OpData,
		},
	}

	d, err := yaml.Marshal(testRecipe)
	require.NoError(t, err)
	recipe2 := &recipe.Recipe{}
	require.NoError(t, yaml.Unmarshal(d, recipe2))
	assert.Equal(t, testRecipe, recipe2)
}
