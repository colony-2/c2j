package compiler

import (
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func validateCatchRecipe(t *testing.T, rec recipe.Recipe) error {
	t.Helper()
	jobCtx, gitCtx := GenerateTestContext()
	returnValue, _, err := ExecuteRecipe(
		workflow.Context{JobContext: &countingJobContext{}, ServiceDependencies2: catchWorkflowContext(&countingJobContext{}).ServiceDependencies2},
		rec,
		map[string]interface{}{},
		jobCtx,
		gitCtx,
		ExecutionOptions{Mode: ExecutionModeValidate, Validation: ValidationOptions{Mode: ValidateAll}},
	)
	_ = returnValue
	return err
}

func TestCatchValidationRejectsUnknownTargetAndTypedFailureField(t *testing.T) {
	const opName = "catch-validation-op"
	registerCatchOp(t, opName)

	err := validateCatchRecipe(t, recipe.Recipe{RecipeImpl: &recipe.RecipeState{
		RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}}},
		StateMachineData: recipe.StateMachineData{States: &recipe.StateMap{
			Initial: recipe.InitialState("work"),
			States: map[string]recipe.State{
				"work": {Node: recipe.Node{NodeImpl: &recipe.NodeOp{
					NodeMetadata: recipe.NodeMetadata{
						Inputs: map[string]interface{}{},
						Catch: []recipe.CatchClause{{
							When: catchExpr(t, `true`),
							To:   "missing",
						}},
					},
					OpData: recipe.OpData{Op: opName},
				}}},
			},
		}},
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "catch_to_unknown_state")

	err = validateCatchRecipe(t, recipe.Recipe{RecipeImpl: &recipe.RecipeOp{
		RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{
			Inputs: map[string]interface{}{},
			Catch: []recipe.CatchClause{{
				When: catchExpr(t, `failure.knd == "task_error"`),
				Continue: &recipe.CatchContinue{Outputs: map[string]interface{}{
					"ok": true,
				}},
			}},
		}},
		OpData: recipe.OpData{Op: opName},
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "catch_when_invalid")
	require.Contains(t, err.Error(), "knd")
}

func TestCatchValidationRejectsRouteOutsideStateMachine(t *testing.T) {
	const opName = "catch-validation-route-outside-op"
	registerCatchOp(t, opName)

	err := validateCatchRecipe(t, recipe.Recipe{RecipeImpl: &recipe.RecipeOp{
		RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{
			Inputs: map[string]interface{}{},
			Catch: []recipe.CatchClause{{
				When: catchExpr(t, `true`),
				To:   "review",
			}},
		}},
		OpData: recipe.OpData{Op: opName},
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "catch_to_without_state_machine")
}

func TestCatchOnlyTransitionValidatesWithTransitionFailure(t *testing.T) {
	const opName = "catch-validation-transition-failure-op"
	registerCatchOp(t, opName)

	err := validateCatchRecipe(t, recipe.Recipe{RecipeImpl: &recipe.RecipeState{
		RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}}},
		StateMachineData: recipe.StateMachineData{
			Outputs: map[string]interface{}{"ok": true},
			States: &recipe.StateMap{
				Initial: recipe.InitialState("work"),
				States: map[string]recipe.State{
					"work": {Node: recipe.Node{NodeImpl: &recipe.NodeOp{
						NodeMetadata: recipe.NodeMetadata{
							Inputs: map[string]interface{}{},
							Catch: []recipe.CatchClause{{
								When: catchExpr(t, `failure.kind == "unknown"`),
								To:   "review",
								Payload: map[string]interface{}{
									"message": "${{ failure.message }}",
								},
							}},
						},
						OpData: recipe.OpData{Op: opName},
					}}},
					"review": {Node: recipe.Node{NodeImpl: &recipe.NodeOp{
						NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{
							"kind":    "${{ transition.failure.kind }}",
							"message": "${{ transition.payload.message }}",
						}},
						OpData: recipe.OpData{Op: opName},
					}}},
				},
			},
		},
	}})
	require.NoError(t, err)
}

func TestCatchAndNormalTransitionValidateBothTransitionShapes(t *testing.T) {
	const opName = "catch-validation-both-transition-op"
	registerCatchOp(t, opName)

	err := validateCatchRecipe(t, recipe.Recipe{RecipeImpl: &recipe.RecipeState{
		RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}}},
		StateMachineData: recipe.StateMachineData{
			Outputs: map[string]interface{}{"ok": true},
			States: &recipe.StateMap{
				Initial: recipe.InitialState("work"),
				States: map[string]recipe.State{
					"work": {
						Node: recipe.Node{NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{
								Inputs: map[string]interface{}{},
								Catch: []recipe.CatchClause{{
									When: catchExpr(t, `true`),
									To:   "review",
								}},
							},
							OpData: recipe.OpData{Op: opName},
						}},
						SingleStateMetadata: recipe.SingleStateMetadata{Transitions: []recipe.Transition{{
							To:   "review",
							When: catchExpr(t, `true`),
						}}},
					},
					"review": {Node: recipe.Node{NodeImpl: &recipe.NodeOp{
						NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{
							"kind": "${{ transition.failure.kind }}",
						}},
						OpData: recipe.OpData{Op: opName},
					}}},
				},
			},
		},
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "failure")
}
