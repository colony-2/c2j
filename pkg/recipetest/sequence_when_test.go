package recipetest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSequenceNodeWhen(t *testing.T) {
	source, err := os.ReadFile("../../testdata/repros/sequence-when-false/recipe.yaml")
	require.NoError(t, err)
	for _, tc := range []struct {
		name       string
		when       string
		expression string
		wantCalls  int
	}{
		{name: "boolean_false", when: "    when: false\n", expression: "false"},
		{name: "quoted_false", when: "    when: 'false'\n", expression: "false"},
		{name: "false_expression", when: "    when: '1 == 2'\n", expression: "1 == 2"},
		{name: "true_control", when: "    when: true\n", expression: "true", wantCalls: 1},
		{name: "omitted_control", wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := strings.Replace(string(source), "    when: false\n", tc.when, 1)
			parsed, err := recipe.LoadRecipeFromString([]byte(content))
			require.NoError(t, err)
			condition := parsed.RecipeImpl.(*recipe.RecipeSequence).Sequence[0].GetMetadata().When
			require.Equal(t, tc.expression, condition.String(), "parser must preserve when")
			allowed, err := condition.AsBool(nil)
			require.NoError(t, err)
			require.Equal(t, tc.wantCalls == 1, allowed, "CEL must evaluate the condition correctly")

			checkSequenceWhenCase(t, content, Case{
				ID: tc.name, Type: "recipe_case",
				Assertions: []Assertion{
					{Type: "output_equals", Path: "finished", Value: true},
					{Type: "op_call_count", NodePath: "sequence-when-false/should-not-run", Value: tc.wantCalls},
				},
			}, "")
		})
	}
}

func TestSequenceNodeWhenContextAndContinuation(t *testing.T) {
	const content = `id: conditional
version: "1.0.0"
input_schema:
  enabled: {type: boolean, required: true}
inputs:
  enabled: '${{ inputs.enabled }}'
vars:
  allow: '${{ inputs.enabled }}'
sequence:
  - id: before
    op: sleep
    inputs: {duration: 1ms}
  - id: conditional
    when: inputs.enabled && vars.allow && sequence.before.outputs.completed
    op: sleep
    inputs: {duration: 1ms}
  - id: after
    op: sleep
    inputs: {duration: 1ms}
outputs:
  finished: '${{ sequence.after.outputs.completed }}'
  conditional_ran: '${{ "conditional" in sequence }}'
`
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			wantCalls := 0
			if enabled {
				wantCalls = 1
			}
			checkSequenceWhenCase(t, content, Case{
				ID: "context", Type: "recipe_case", Inputs: map[string]any{"enabled": enabled},
				Assertions: []Assertion{
					{Type: "output_equals", Path: "finished", Value: true},
					{Type: "output_equals", Path: "conditional_ran", Value: enabled},
					{Type: "op_call_count", NodePath: "conditional/before", Value: 1},
					{Type: "op_call_count", NodePath: "conditional/conditional", Value: wantCalls},
					{Type: "op_call_count", NodePath: "conditional/after", Value: 1},
				},
			}, "")
		})
	}
}

func TestSequenceNodeWhenSkipsPreparation(t *testing.T) {
	const content = `id: skipped
version: "1.0.0"
sequence:
  - id: task
    when: false
    workspace: {cell: '${{ inputs.missing_cell }}'}
    vars: {bad: '${{ inputs.missing_var }}'}
    op: sleep
    inputs: {duration: '${{ inputs.missing_duration }}'}
  - id: nested
    when: false
    inputs: {bad: '${{ inputs.missing_input }}'}
    sequence:
      - id: unreachable
        op: sleep
        inputs: {duration: 1ms}
outputs:
  finished: true
`
	checkSequenceWhenCase(t, content, Case{
		ID: "skip-preparation", Type: "recipe_case",
		// Exercise execution; all-path validation intentionally checks false branches.
		Options: map[string]any{"validation_mode": "structure_only"},
		Assertions: []Assertion{
			{Type: "output_equals", Path: "finished", Value: true},
			{Type: "op_call_count", NodePath: "skipped/task", Value: 0},
			{Type: "op_call_count", NodePath: "skipped/nested/unreachable", Value: 0},
		},
	}, "")
}

func TestSequenceNodeWhenInvalidCondition(t *testing.T) {
	for _, condition := range []string{"inputs.", "inputs.missing", "42"} {
		t.Run(condition, func(t *testing.T) {
			content := fmt.Sprintf(`id: invalid
version: "1.0.0"
sequence:
  - id: guarded
    when: %q
    op: sleep
    inputs: {duration: 1ms}
outputs: {finished: true}
`, condition)
			checkSequenceWhenCase(t, content, Case{
				ID: "invalid", Type: "recipe_case",
				Options: map[string]any{"validation_mode": "structure_only"},
			}, `failed to evaluate when for node "guarded"`)
		})
	}
}

func checkSequenceWhenCase(t *testing.T, content string, c Case, wantError string) {
	t.Helper()
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("runtime=%t", native), func(t *testing.T) {
			c := c
			if native {
				c.Runtime = &RuntimeCase{}
			} else {
				// The fast runner requires a response for each possible sleep call.
				for range 3 {
					c.Mocks.Ops = append(c.Mocks.Ops, OpMock{
						Match:    OpMockMatch{Op: "sleep"},
						Behavior: MockBehavior{Mode: "return", Outputs: map[string]any{"completed": true}},
					})
				}
			}
			result := RunCase(context.Background(), HarnessOptions{}, "sequence-when-repro",
				TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: content}, c,
				ExecutionOptions{Timeout: "10s"})
			if wantError != "" {
				require.Equal(t, "failed", result.Status)
				assert.Contains(t, result.FailureReason, wantError)
				assert.Empty(t, result.Diagnostics.Calls)
				if native {
					require.NotNil(t, result.Runtime)
					assert.Empty(t, result.Runtime.Calls)
				}
				return
			}
			assert.Equal(t, "passed", result.Status, "assertions: %+v; failure: %s", result.Assertions, result.FailureReason)
		})
	}
}
