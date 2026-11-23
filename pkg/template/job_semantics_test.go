package template

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExactJobQuotedMarkerEvaluation(t *testing.T) {
	rc := newRecipeCtx(t, map[string]interface{}{"goal_id": "{{ literal }}"})
	for _, expression := range []string{
		`context.workflow.goal_id != '{{ literal }}'`,
		`'${{ literal }}' != context['workflow']['goal_id']`,
		`sequence.prior.jobs.goal_ids.size() > 0 && '{{ literal }}' != ''`,
	} {
		t.Run(expression, func(t *testing.T) {
			_, err := rc.ResolveTemplateWithMode(expression, ModePureCEL)
			require.ErrorContains(t, err, "retired live goal context")
			_, err = rc.EvaluateCEL(expression)
			require.ErrorContains(t, err, "retired live goal context")
		})
	}
	for _, expression := range []string{
		`'${{ context.workflow.goal_id }}' != ''`,
		`inputs.goal_id == '{{ literal }}'`,
	} {
		value, err := rc.ResolveTemplateWithMode(expression, ModePureCEL)
		require.NoError(t, err)
		require.Equal(t, true, value)
	}
}

func TestExactJobQuotedMarkerInterpolation(t *testing.T) {
	rc := newRecipeCtx(t, map[string]interface{}{"goal_id": "application-data"})
	for _, expression := range []string{
		`${{ context.workflow.goal_id }}`,
		`${{ '}}' + context.workflow.goal_id }}`,
		`prefix ${{ inputs.goal_id }} ${{ "}}" + context['workflow']['goal_id'] }} suffix`,
	} {
		_, err := rc.ResolveTemplateWithMode(expression, ModeInterpolation)
		require.ErrorContains(t, err, "retired live goal context", expression)
	}
	for expression, want := range map[string]string{
		`${{ 'context.workflow.goal_id' }}`:    "context.workflow.goal_id",
		`${{ '}} context.workflow.goal_id' }}`: "}} context.workflow.goal_id",
		`${{ '}}' + inputs.goal_id }}`:         "}}application-data",
		`prefix ${{ inputs.goal_id }} suffix`:  "prefix application-data suffix",
	} {
		value, err := rc.ResolveTemplateWithMode(expression, ModeInterpolation)
		require.NoError(t, err, expression)
		require.Equal(t, want, value)
	}
}
