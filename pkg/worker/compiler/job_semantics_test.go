package compiler

import (
	"context"
	"github.com/colony-2/c2j/pkg/ops"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/require"
)

func TestExactJobSnapshotVersion(t *testing.T) {
	for _, version := range []string{"", `,"job_semantics_version":1`, `,"job_semantics_version":2`} {
		start, _, err := decodeRecipeInput([]byte(`{"tenantId":"t","recipe":"build"`+version+`}`), nil)
		require.NoError(t, err)
		if version == "" {
			require.Zero(t, start.JobSemanticsVersion)
		} else {
			require.NotZero(t, start.JobSemanticsVersion)
		}
		require.Equal(t, start.JobSemanticsVersion, start.JobContext.JobSemanticsVersion)
	}
	for _, version := range []string{`null`, `"2"`, `2.0`, `3`, `true`} {
		_, _, err := decodeRecipeInput([]byte(`{"job_semantics_version":`+version+`}`), nil)
		require.Error(t, err)
	}
	_, _, err := decodeRecipeInput([]byte(`{"job_semantics_version":2,"context":{"job_semantics_version":1}}`), nil)
	require.ErrorContains(t, err, "contradictory")
}

func TestExactJobStaticSyntax(t *testing.T) {
	noop := ops.NewOp().WithType("noop").AddStep("noop", ops.NewStep(func(context.Context, map[string]any) (map[string]any, error) { return nil, nil })).BuildOrPanic()
	withRegisteredOps(t, append(recipeops.GetOps(), noop)...)

	for _, body := range []string{
		"id: test\nsequence:\n - id: earlier\n   op: noop\n - id: invalid\n   op: recipe.await_result\n   inputs: {target: {goal: G}}\n",
		"id: test\nop: recipe.await_result\ninputs: {target: {job: A}, wait: {on: goal}}\n",
		"id: test\nop: noop\ninputs: {goal: '${{ context.workflow.goal_id }}'}\n",
	} {
		rec, err := recipe.LoadRecipeFromString([]byte(body))
		require.NoError(t, err)
		require.Error(t, recipe.ValidateJobSemantics(*rec, 2), body)
	}
	rec, err := recipe.LoadRecipeFromString([]byte("id: app\nop: noop\ninputs: {application: {op: recipe.await_result, inputs: {goal_id: opaque}}}\n"))
	require.NoError(t, err)
	require.NoError(t, recipe.ValidateJobSemantics(*rec, 2))
}
