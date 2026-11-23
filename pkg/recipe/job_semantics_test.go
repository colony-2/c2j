package recipe

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExactJobRawCELFields(t *testing.T) {
	for name, body := range map[string]string{
		"root":            "id: r\nwhen: EXPR\nsequence: []\n",
		"nested":          "id: r\nsequence:\n- when: EXPR\n  sequence: []\n",
		"catch":           "id: r\nsequence: []\ncatch: [{when: EXPR, continue: {}}]\n",
		"defs":            "id: r\nsequence: []\ndefs:\n  unused:\n    when: EXPR\n    sequence: []\n",
		"initial":         "id: r\nstate:\n  initial: [{when: EXPR, to: done}]\n  states:\n    done: {sequence: []}\n",
		"transition":      "id: r\nstate:\n  initial: done\n  states:\n    done:\n      sequence: []\n      transitions: [{when: EXPR, to: done}]\n",
		"switch":          "id: r\nstate:\n  initial: done\n  states:\n    done:\n      sequence: []\n      transitions:\n        switch: EXPR\n        cases: [{value: 'true', to: done}]\n",
		"child":           "id: r\nchild_group:\n  wait: {on: job}\n  children: [{key: one, recipe: unused, when: EXPR}]\n",
		"generated-child": "id: r\nchild_group:\n  wait: {on: job}\n  children_from: []\n  child: {recipe: unused, when: EXPR}\n",
	} {
		for _, expr := range []string{"context.workflow.goal_id != ''", "sequence.prior.jobs.goal_ids.size() > 0", "states['prior'].jobs['goal_ids'].size() > 0", "'context.workflow.goal_id' != ''", "inputs.goal_id != ''", "context.workflow.goal_id != '{{ literal }}'", "sequence.prior.jobs.goal_ids.size() > 0 && '{{ literal }}' != ''", "states['prior'].jobs['goal_ids'].size() > 0 && '{{ literal }}' != ''"} {
			t.Run(name+"/"+expr, func(t *testing.T) {
				rec, err := LoadRecipeFromString([]byte(strings.ReplaceAll(body, "EXPR", quoteCEL(expr))))
				require.NoError(t, err)
				for _, version := range []int{0, 1, 2} {
					err = ValidateJobSemantics(*rec, version)
					if strings.HasPrefix(expr, "'") || strings.HasPrefix(expr, "inputs.") {
						require.NoError(t, err)
					} else {
						require.ErrorContains(t, err, "retired live goal context")
					}
				}
			})
		}
	}
}

func TestExactJobRawCELOpaqueData(t *testing.T) {
	rec, err := LoadRecipeFromString([]byte(`id: r
sequence: []
inputs: {when: "context.workflow.goal_id != ''"}
vars: {when: "context.workflow.goal_id != ''"}
outputs: {when: "context.workflow.goal_id != ''"}
artifacts: {when: "context.workflow.goal_id != ''"}
catch:
- to: done
  payload: {when: "context.workflow.goal_id != ''"}
`))
	require.NoError(t, err)
	require.NoError(t, ValidateJobSemantics(*rec, 2))
	group, err := LoadRecipeFromString([]byte(`id: r
child_group:
  children: [{key: one, recipe: unused, inputs: {when: "context.workflow.goal_id != ''"}}]
  aggregate:
    shape: {when: "context.workflow.goal_id != ''"}
`))
	require.NoError(t, err)
	require.NoError(t, ValidateJobSemantics(*group, 2))
}

func quoteCEL(expr string) string { data, _ := json.Marshal(expr); return string(data) }

func TestExactJobQuotedMarkerConditions(t *testing.T) {
	for name, body := range map[string]string{
		"root":                  "id: r\nwhen: EXPR\nsequence: []\n",
		"nested":                "id: r\nsequence:\n- when: EXPR\n  sequence: []\n",
		"catch":                 "id: r\nsequence: []\ncatch: [{when: EXPR, continue: {}}]\n",
		"definition-named-when": "id: r\nsequence: []\ndefs:\n  when: {when: EXPR, sequence: []}\n",
		"transition":            "id: r\nstate:\n  initial: done\n  states:\n    when: {sequence: []}\n    done: {sequence: [], transitions: [{when: EXPR, to: when}]}\n",
	} {
		for _, test := range []struct {
			expr    string
			retired bool
		}{
			{`'${{ literal }}' != context['workflow']['goal_id']`, true},
			{`'${{ context.workflow.goal_id }}' != ''`, false},
			{`inputs.goal_id != '{{ literal }}'`, false},
		} {
			t.Run(name+"/"+test.expr, func(t *testing.T) {
				rec, err := LoadRecipeFromString([]byte(strings.ReplaceAll(body, "EXPR", quoteCEL(test.expr))))
				require.NoError(t, err)
				for _, version := range []int{0, 1, 2} {
					err := ValidateJobSemantics(*rec, version)
					if test.retired {
						require.ErrorContains(t, err, "retired live goal context")
					} else {
						require.NoError(t, err)
					}
				}
			})
		}
	}
}

func TestExactJobQuotedMarkerTemplateData(t *testing.T) {
	for name, body := range map[string]string{
		"inputs":                   "id: r\nsequence: []\ninputs: {when: EXPR}\n",
		"vars":                     "id: r\nsequence: []\nvars: {when: EXPR}\n",
		"outputs":                  "id: r\nsequence: []\noutputs: {when: EXPR}\n",
		"artifacts":                "id: r\nsequence: []\nartifacts: {when: EXPR}\n",
		"catch-payload":            "id: r\nsequence: []\ncatch: [{to: done, payload: {when: EXPR}}]\n",
		"child-inputs":             "id: r\nchild_group:\n  wait: {on: job}\n  children: [{key: one, recipe: child, inputs: {when: EXPR}}]\n",
		"child-template-condition": "id: r\nchild_group:\n  wait: {on: job}\n  children: [{key: one, recipe: child, when: EXPR}]\n",
		"aggregate":                "id: r\nchild_group:\n  wait: {on: job}\n  aggregate: {shape: {when: EXPR}}\n",
		"definition-named-when":    "id: r\nsequence: []\ndefs:\n  when: {sequence: [], inputs: {when: EXPR}}\n",
		"state-named-when":         "id: r\nstate:\n  initial: when\n  states:\n    when: {sequence: [], inputs: {when: EXPR}}\n",
	} {
		for _, test := range []struct {
			expr    string
			retired bool
		}{
			{`${{ '}}' + context.workflow.goal_id }}`, true},
			{`${{ '}} context.workflow.goal_id' }}`, false},
		} {
			t.Run(name+"/"+test.expr, func(t *testing.T) {
				rec, err := LoadRecipeFromString([]byte(strings.ReplaceAll(body, "EXPR", quoteCEL(test.expr))))
				require.NoError(t, err)
				err = ValidateJobSemantics(*rec, 2)
				if test.retired {
					require.ErrorContains(t, err, "retired live goal context")
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}
