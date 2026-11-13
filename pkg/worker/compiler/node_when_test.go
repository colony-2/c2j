package compiler

import (
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/require"
)

func TestNodeWhenValidationModes(t *testing.T) {
	const source = `id: validate-when
sequence:
  - id: skipped
    when: false
    sequence: []
    outputs:
      invalid: '${{ inputs.missing }}'
outputs: {}
`
	for _, tc := range []struct {
		name      string
		mode      ValidationMode
		condition string
		wantError string
	}{
		{name: "all_checks_false_body", mode: ValidateAll, condition: "false", wantError: "missing"},
		{name: "path_skips_false_body", mode: ValidatePathOnly, condition: "false"},
		{name: "all_rejects_non_boolean", mode: ValidateAll, condition: "'42'", wantError: "failed to evaluate when"},
		{name: "path_rejects_non_boolean", mode: ValidatePathOnly, condition: "'42'", wantError: "failed to evaluate when"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := recipe.LoadRecipeFromString([]byte(strings.Replace(source, "when: false", "when: "+tc.condition, 1)))
			require.NoError(t, err)
			job, git := GenerateTestContext()
			inner := &countingJobContext{}
			_, _, err = ExecuteRecipe(newWorkflowContext(inner), *r, nil, job, git, ExecutionOptions{
				Mode: ExecutionModeValidate, Validation: ValidationOptions{Mode: tc.mode},
			})
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}
			require.Zero(t, inner.calls)
		})
	}
}
