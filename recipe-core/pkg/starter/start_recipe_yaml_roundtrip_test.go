package starter

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/recipe-core/pkg/ops"
	"github.com/colony-2/c2j/recipe-core/pkg/recipe"
	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"
)

func registerLooseOp(t *testing.T, opType string) {
	t.Helper()
	op := ops.NewOp().
		WithType(opType).
		WithDescription("test op").
		WithVersion("0.0.0").
		AddStep("main", ops.NewStep(func(ctx context.Context, in map[string]any) (struct{}, error) {
			_ = ctx
			_ = in
			return struct{}{}, nil
		})).
		BuildOrPanic()
	ops.Register(op)
}

func TestStartRecipeJob_YAML_RoundTrip_PreservesTransitionWhenExpressions(t *testing.T) {
	// These are placeholder op registrations so recipe parsing doesn't fail during YAML decode.
	registerLooseOp(t, "recipe.run_and_get_result")
	registerLooseOp(t, "command_execution")

	origYAML := []byte(`
id: new-ticket
version: 0.1.4
state:
  initial: triage
  states:
    triage:
      op: recipe.run_and_get_result
      transitions:
        - to: requirements_planning
          when: "true"
      inputs: {}
    requirements_planning:
      op: recipe.run_and_get_result
      transitions:
        - to: maybe_reassign
          when: "true"
      inputs: {}
    maybe_reassign:
      op: command_execution
      transitions:
        - to: reassign_create
          when: "has(states.triage.outputs.outputs.cell_is_appropriate) && !states.triage.outputs.outputs.cell_is_appropriate && has(states.triage.outputs.outputs.recommended_cell_is_valid) && states.triage.outputs.outputs.recommended_cell_is_valid"
        - to: requirements_note
          when: "true"
      inputs: {}
`)

	orig, err := recipe.LoadRecipeFromString(origYAML)
	require.NoError(t, err)
	origState := orig.RecipeImpl.(*recipe.RecipeState)
	require.Equal(t, "true", origState.States.States["triage"].Transitions[0].When.String())
	require.Equal(t, "true", origState.States.States["maybe_reassign"].Transitions[1].When.String())

	// This matches StartRecipeJob's YAML serialization behavior (`yaml.Marshal(&r)`).
	val := *orig
	roundtrippedYAML, err := yamlv3.Marshal(&val)
	require.NoError(t, err)

	back, err := recipe.LoadRecipeFromString(roundtrippedYAML)
	require.NoError(t, err)
	backState := back.RecipeImpl.(*recipe.RecipeState)

	require.Equal(t, "true", backState.States.States["triage"].Transitions[0].When.String())
	require.Equal(t, "true", backState.States.States["maybe_reassign"].Transitions[1].When.String())
	require.Equal(t,
		"has(states.triage.outputs.outputs.cell_is_appropriate) && !states.triage.outputs.outputs.cell_is_appropriate && has(states.triage.outputs.outputs.recommended_cell_is_valid) && states.triage.outputs.outputs.recommended_cell_is_valid",
		backState.States.States["maybe_reassign"].Transitions[0].When.String(),
	)
}

