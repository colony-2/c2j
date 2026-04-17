package compiler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestParseExampleRecipes(t *testing.T) {
	// Find the examples directory relative to this test file
	examplesDir := filepath.Join("..", "..", "examples")

	// Check if examples directory exists
	if _, err := os.Stat(examplesDir); os.IsNotExist(err) {
		t.Skip("Examples directory not found")
	}

	tests := []struct {
		name     string
		filename string
	}{
		{
			name:     "unified_data_pipeline",
			filename: "unified_data_pipeline.yaml",
		},
		{
			name:     "template_features",
			filename: "template_features.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recipePath := filepath.Join(examplesDir, tt.filename)

			// Check if recipe file exists
			if _, err := os.Stat(recipePath); os.IsNotExist(err) {
				t.Skipf("Recipe file not found: %s", recipePath)
			}

			// Read recipe file
			recipeData, err := os.ReadFile(recipePath)
			require.NoError(t, err, "Failed to read recipe file")

			// Parse recipe YAML
			var recipeDef recipe.Recipe
			err = yaml.Unmarshal(recipeData, &recipeDef)
			require.NoError(t, err, "Failed to parse recipe YAML")

			// Basic validation
			metadata := recipeDef.RecipeImpl.GetMetadata()
			assert.NotEmpty(t, metadata.Version, "Recipe should have a version")

			// Check that recipe has at least one node type defined
			hasNode := false
			if recipeDef.RecipeImpl != nil {
				switch impl := recipeDef.RecipeImpl.(type) {
				case *recipe.RecipeOp:
					hasNode = impl.Op != ""
				case *recipe.RecipeSequence:
					hasNode = len(impl.Sequence) > 0
				case *recipe.RecipeState:
					hasNode = impl.States != nil
				}
			}
			assert.True(t, hasNode, "Recipe should define at least one node type")
		})
	}
}

func TestRecipeStructureValidation(t *testing.T) {
	tests := []struct {
		name      string
		recipe    recipe.Recipe
		wantError bool
	}{
		{
			name: "valid_operation_recipe",
			recipe: recipe.Recipe{
				RecipeImpl: &recipe.RecipeOp{
					RecipeMetadata: recipe.RecipeMetadata{
						NodeMetadata: recipe.NodeMetadata{
							Inputs: map[string]interface{}{
								"param": "value",
							},
						},
						Version: "1.0",
					},
					OpData: recipe.OpData{
						Op: "test_activity",
					},
				},
			},
			wantError: false,
		},
		{
			name: "valid_sequence_recipe",
			recipe: recipe.Recipe{
				RecipeImpl: &recipe.RecipeSequence{
					RecipeMetadata: recipe.RecipeMetadata{
						Version: "1.0",
					},
					SequenceData: recipe.SequenceData{
						Sequence: []recipe.Node{
							{
								NodeImpl: &recipe.NodeOp{
									NodeMetadata: recipe.NodeMetadata{
										ID: "step1",
									},
									OpData: recipe.OpData{
										Op: "activity1",
									},
								},
							},
							{
								NodeImpl: &recipe.NodeOp{
									NodeMetadata: recipe.NodeMetadata{
										ID: "step2",
									},
									OpData: recipe.OpData{
										Op: "activity2",
									},
								},
							},
						},
					},
				},
			},
			wantError: false,
		},
		{
			name: "recipe_with_shared_definitions",
			recipe: recipe.Recipe{
				RecipeImpl: &recipe.RecipeSequence{
					RecipeMetadata: recipe.RecipeMetadata{
						Version: "1.0",
						Defs: map[string]recipe.Node{
							"shared_op": {
								NodeImpl: &recipe.NodeOp{
									NodeMetadata: recipe.NodeMetadata{
										Inputs: map[string]interface{}{
											"param": "value",
										},
									},
									OpData: recipe.OpData{
										Op: "shared_activity",
									},
								},
							},
						},
					},
					SequenceData: recipe.SequenceData{
						Sequence: []recipe.Node{
							{
								NodeImpl: &recipe.NodeShared{
									Shared: "shared_op",
								},
							},
						},
					},
				},
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Basic structural validation
			metadata := tt.recipe.RecipeImpl.GetMetadata()
			assert.NotEmpty(t, metadata.Version, "Recipe should have a version")

			// Check recipe structure
			nodeCount := 0
			if tt.recipe.RecipeImpl != nil {
				switch impl := tt.recipe.RecipeImpl.(type) {
				case *recipe.RecipeOp:
					if impl.Op != "" {
						nodeCount++
					}
				case *recipe.RecipeSequence:
					if len(impl.Sequence) > 0 {
						nodeCount++
					}
				case *recipe.RecipeState:
					if impl.States != nil {
						nodeCount++
					}
				}
			}

			// A recipe should define exactly one type
			if !tt.wantError {
				assert.Equal(t, 1, nodeCount, "Recipe should define exactly one of: op, sequence, or states")
			}
		})
	}
}
