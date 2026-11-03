package recipetest

import (
	"context"
	"testing"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestStructuredInputsInRecipeTestHarness(t *testing.T) {
	target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `schema_version: 2
id: structured
input_schema:
  annotations:
    type: array
    items:
      type: object
      properties:
        path: {type: string, required: true}
        title: {type: string, default_value: Design}
inputs: {annotations: '${{ inputs.annotations }}'}
sequence: []
outputs: {annotations: '${{ inputs.annotations }}'}
`}
	for _, runtime := range []*RuntimeCase{nil, {}} {
		c := Case{ID: "structured", Type: "recipe_case", Runtime: runtime, Inputs: map[string]any{
			"annotations": []any{map[string]any{"path": "design.md"}},
		}}
		opts := HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}
		validation := ValidateCase(context.Background(), opts, "structured-tests", target, c)
		require.True(t, validation.Valid, "%+v", validation.Errors)
		result := RunCase(context.Background(), opts, "structured-tests", target, c, ExecutionOptions{})
		require.Equal(t, "passed", result.Status, result.FailureReason)
		require.Equal(t, []any{map[string]any{"path": "design.md", "title": "Design"}}, result.Outputs["annotations"])
		c.Inputs = map[string]any{"annotations": []any{map[string]any{"path": false}}}
		result = RunCase(context.Background(), opts, "structured-tests", target, c, ExecutionOptions{})
		require.NotEqual(t, "passed", result.Status)
		require.Contains(t, result.FailureReason, "annotations[0].path")
	}
}
