package jobsemantics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExactJobVersionInterpretation(t *testing.T) {
	for _, v := range []int{0, 1, 2} {
		require.NoError(t, Validate(v))
	}
	for _, v := range []int{-1, 3} {
		require.Error(t, Validate(v))
	}
	for _, op := range []string{"recipe.await_result", "recipe.await_result_soft", "recipe.run_and_get_result"} {
		for _, version := range []int{0, 1} {
			require.ErrorContains(t, CheckOperation(version, op, nil, true), "legacy implicit goal")
			require.NoError(t, CheckOperation(version, op, map[string]any{"wait": map[string]any{"on": "job"}}, true))
		}
		require.NoError(t, CheckOperation(2, op, nil, true))
		require.ErrorContains(t, CheckOperation(2, op, map[string]any{"target": map[string]any{"goal": "G"}}, true), "goal targets")
		require.Error(t, CheckOperation(2, op, map[string]any{"wait": map[string]any{"on": "goal"}}, false))
		require.NoError(t, CheckOperation(2, op, map[string]any{"wait": map[string]any{"on": "${{ inputs.mode }}"}}, false))
		require.Error(t, CheckOperation(2, op, map[string]any{"wait": map[string]any{"on": "${{ inputs.mode }}"}}, true))
	}
	require.NoError(t, CheckOperation(2, "command_execution", map[string]any{"goal_id": "application-data"}, true))
	require.Error(t, CheckOperation(2, "recipes.run", map[string]any{"recipes": []any{map[string]any{"wait": map[string]any{"on": "goal"}}}}, true))
	for _, version := range []int{0, 1} {
		require.ErrorContains(t, CheckOperation(version, "recipe.child_group.run_and_get_result", map[string]any{"children": []any{map[string]any{"recipe": "child"}}}, true), "legacy implicit goal")
		require.NoError(t, CheckOperation(version, "recipe.child_group.run_and_get_result", map[string]any{"wait": map[string]any{"on": "job"}, "children": []any{map[string]any{"recipe": "child"}}}, true))
	}
}

func TestExactJobLiveExpressionsAndOpaqueData(t *testing.T) {
	for _, expression := range []string{
		`context.workflow.goal_id`,
		`context['workflow']["goal_id"]`,
		`sequence.child.jobs.goal_ids`,
		`states['child'].runs[0]['jobs']['goal_ids']`,
	} {
		require.ErrorContains(t, CheckExpression(expression), "retired live goal context", expression)
	}
	for _, expression := range []string{
		`'context.workflow.goal_id'`,
		`inputs.goal_id`,
		`inputs.jobs.goal_ids`,
		`sequence.child.outputs.jobs.goal_ids`,
	} {
		require.NoError(t, CheckExpression(expression), expression)
	}
}

func TestExactJobQuotedMarkerExpressions(t *testing.T) {
	for _, expression := range []string{
		`context.workflow.goal_id != '{{ literal }}'`,
		`'${{ literal }}' != context['workflow']['goal_id']`,
		`sequence.prior.jobs.goal_ids.size() > 0 && '{{ literal }}' != ''`,
		`states['prior'].jobs['goal_ids'].size() > 0 && '{{ literal }}' != ''`,
	} {
		t.Run(expression, func(t *testing.T) {
			require.ErrorContains(t, CheckExpression(expression), "retired live goal context")
		})
	}
	for _, expression := range []string{
		`'${{ context.workflow.goal_id }}' != ''`,
		`inputs.goal_id != '{{ literal }}'`,
		`sequence.prior.outputs.jobs.goal_ids != '{{ literal }}'`,
	} {
		require.NoError(t, CheckExpression(expression), expression)
	}
}

func TestExactJobQuotedMarkerTemplates(t *testing.T) {
	for _, template := range []string{
		`${{ context.workflow.goal_id }}`,
		`before ${{ '}}' + context.workflow.goal_id }} after`,
		`${{ inputs.goal_id }} ${{ "}}" + context['workflow']['goal_id'] }}`,
		`${{ '}}' != '' && sequence.prior.jobs.goal_ids.size() > 0 }}`,
	} {
		require.ErrorContains(t, CheckTemplate(template), "retired live goal context", template)
	}
	for _, template := range []string{
		`context.workflow.goal_id`,
		`${{ 'context.workflow.goal_id' }}`,
		`printf 'context.workflow.goal_id' '${{ inputs.goal_id }}'`,
		`${{ '}}' + inputs.goal_id }}`,
		`${{ '}} context.workflow.goal_id' }}`,
	} {
		require.NoError(t, CheckTemplate(template), template)
	}
}

func TestExactJobQuotedMarkerGrammar(t *testing.T) {
	// A template is not a raw condition. Reject it before submission/effects,
	// while preserving the child condition's explicitly supported template form.
	require.ErrorContains(t, CheckExpression(`${{ context.workflow.goal_id }}`), "invalid raw CEL expression")
	require.ErrorContains(t, CheckChildCondition(`${{ context.workflow.goal_id }}`), "retired live goal context")
	require.ErrorContains(t, CheckChildCondition(`context.workflow.goal_id != '{{ literal }}'`), "retired live goal context")
	require.NoError(t, CheckChildCondition(`${{ inputs.enabled }}`))
	require.NoError(t, CheckTemplate(`{{ printf "%s" .inputs.name }}`))
	require.NoError(t, CheckExpression(""))
}
