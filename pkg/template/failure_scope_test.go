package template

import (
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/require"
)

func testFailure() *recipe.RuntimeFailure {
	return &recipe.RuntimeFailure{
		Kind:      recipe.FailureKindTaskError,
		Code:      "E_BAD",
		Message:   "task failed badly",
		Retryable: true,
		Node: recipe.FailureNode{
			ID:   "build",
			Path: "root/build",
			Type: recipe.FailureNodeOp,
			Op:   "command_execution",
		},
		Cause: &recipe.RuntimeFailure{
			Kind:    recipe.FailureKindTimeout,
			Code:    "timeout_total",
			Message: "root timeout",
			Node: recipe.FailureNode{
				ID:   "root",
				Path: "root",
				Type: recipe.FailureNodeStateMachine,
			},
		},
	}
}

func TestFailureScopeWorksInCELAndTemplates(t *testing.T) {
	ctx := newRecipeCtx(t, nil).WithFailure(testFailure())

	ok, err := ctx.EvaluateCEL(`failure.kind == "task_error" && failure_has_code(failure, "E_BAD") && failure_message_contains(failure, "badly")`)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = ctx.EvaluateCEL(`failure_message_matches(failure, "failed\\s+badly") && failure_originates_from(failure, "build")`)
	require.NoError(t, err)
	require.True(t, ok)

	resolved, err := ctx.ResolveMap(map[string]interface{}{
		"cel_message": "${{ failure.message }}",
		"go_message":  "{{ failure.message }}",
		"root_code":   "${{ failure_root_cause(failure).code }}",
	})
	require.NoError(t, err)
	require.Equal(t, "task failed badly", resolved["cel_message"])
	require.Equal(t, "task failed badly", resolved["go_message"])
	require.Equal(t, "timeout_total", resolved["root_code"])
}

func TestFailureScopeRejectsUnknownCELField(t *testing.T) {
	ctx := newRecipeCtx(t, nil).WithFailure(testFailure())

	_, err := ctx.EvaluateCEL(`failure.knd == "task_error"`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "knd")
}

func TestTransitionFailureVisibleToTargetState(t *testing.T) {
	ctx := newStateCtx(t, newStateMachineCtx(t, newRecipeCtx(t, nil), "sm", map[string]interface{}{}), "review")
	ctx.TemplateData.Transition = NewFailureTransitionData("work", "review", map[string]interface{}{
		"note": "check this",
	}, testFailure())

	resolved, err := ctx.ResolveMap(map[string]interface{}{
		"kind":    "${{ transition.failure.kind }}",
		"message": "{{ transition.failure.message }}",
		"note":    "${{ transition.payload.note }}",
	})
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{
		"kind":    "task_error",
		"message": "task failed badly",
		"note":    "check this",
	}, resolved)
}
