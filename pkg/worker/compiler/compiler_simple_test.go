package compiler

import (
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/stretchr/testify/assert"
)

func TestSimpleRecipeStructure(t *testing.T) {
	// Create a simple recipe definition using unified format
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

	// Create activity registry
	registry, err := ops.NewActivityRegistry()
	assert.NoError(t, err)

	// Verify the structure
	assert.NotNil(t, node)
	nodeOp := node.NodeImpl.(*recipe.NodeOp)
	assert.Equal(t, "test-recipe", nodeOp.NodeMetadata.ID)
	assert.Equal(t, "test_activity", nodeOp.Op)
	assert.NotNil(t, registry)
}

func TestSequenceRecipeStructure(t *testing.T) {
	// Test sequence structure instead of parallel
	node := &recipe.Node{
		NodeImpl: &recipe.NodeSequence{
			NodeMetadata: recipe.NodeMetadata{
				ID:   "sequence-recipe",
				Desc: "Sequence recipe",
			},
			SequenceData: recipe.SequenceData{
				Sequence: []recipe.Node{
					{
						NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{
								ID: "task_a",
								Inputs: map[string]interface{}{
									"type": "function",
								},
							},
							OpData: recipe.OpData{
								Op: "activity_a",
							},
						},
					},
					{
						NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{
								ID: "task_b",
								Inputs: map[string]interface{}{
									"type": "function",
								},
							},
							OpData: recipe.OpData{
								Op: "activity_b",
							},
						},
					},
				},
			},
		},
	}

	// Verify the structure
	assert.NotNil(t, node)
	nodeSeq := node.NodeImpl.(*recipe.NodeSequence)
	assert.Len(t, nodeSeq.Sequence, 2)
	node1 := nodeSeq.Sequence[0].NodeImpl.(*recipe.NodeOp)
	node2 := nodeSeq.Sequence[1].NodeImpl.(*recipe.NodeOp)
	assert.Equal(t, "task_a", node1.NodeMetadata.ID)
	assert.Equal(t, "task_b", node2.NodeMetadata.ID)
}

func TestRecipeWithSharedDefinitions(t *testing.T) {
	recipeDef := &recipe.Recipe{
		RecipeImpl: &recipe.RecipeSequence{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{
					ID: "shared-recipe",
				},
				Version: "1.0",
				Defs: map[string]recipe.Node{
					"my_llm": {
						NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{
								Inputs: map[string]interface{}{
									"type":  "ai_prompt",
									"model": "gpt-4",
								},
							},
							OpData: recipe.OpData{
								Op: "llm",
							},
						},
					},
				},
			},
			SequenceData: recipe.SequenceData{
				Sequence: []recipe.Node{
					{
						NodeImpl: &recipe.NodeShared{
							Shared: "my_llm",
						},
					},
				},
			},
		},
	}

	// Verify the structure
	assert.NotNil(t, recipeDef)
	recipeSeq := recipeDef.RecipeImpl.(*recipe.RecipeSequence)
	assert.Equal(t, "1.0", recipeSeq.RecipeMetadata.Version)
	assert.NotNil(t, recipeSeq.RecipeMetadata.Defs)
	assert.Len(t, recipeSeq.Sequence, 1)
	assert.Contains(t, recipeSeq.RecipeMetadata.Defs, "my_llm")
}
