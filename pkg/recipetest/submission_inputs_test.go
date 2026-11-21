package recipetest

import (
	"context"
	"fmt"
	"testing"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestRecipeSubmissionInputsNativeHarness(t *testing.T) {
	for _, runtime := range []*RuntimeCase{nil, {}} {
		for declared := 0; declared < 4; declared++ {
			t.Run(fmt.Sprintf("runtime=%t/declared=%d", runtime != nil, declared), func(t *testing.T) {
				schema := "input_schema:\n  value: {type: string, required: true}\n"
				want := map[string]any{"value": "run"}
				for i, key := range []string{"title", "prompt"} {
					if declared&(1<<i) != 0 {
						schema += "  " + key + ": {type: string}\n"
						want[key] = key
					}
				}
				target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: "id: submission-inputs\n" + schema + "inputs: {all: '${{ inputs }}'}\nsequence: []\noutputs: {all: '${{ inputs.all }}'}\n"}
				c := Case{ID: "submission-inputs", Type: "recipe_case", Runtime: runtime, Inputs: map[string]any{"value": "run", "title": "title", "prompt": "prompt"}}
				opts := HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}
				validation := ValidateCase(context.Background(), opts, "submission-inputs", target, c)
				require.True(t, validation.Valid, "%+v", validation.Errors)
				result := RunCase(context.Background(), opts, "submission-inputs", target, c, ExecutionOptions{})
				require.Equal(t, "passed", result.Status, result.FailureReason)
				require.Equal(t, want, result.Outputs["all"])
				c.Inputs["extra"] = "must fail"
				validation = ValidateCase(context.Background(), opts, "submission-inputs", target, c)
				if runtime == nil {
					require.False(t, validation.Valid)
				} else {
					require.True(t, validation.Valid)
					require.Contains(t, fmt.Sprint(validation.Warnings), "execution_validation_deferred")
				}
				result = RunCase(context.Background(), opts, "submission-inputs", target, c, ExecutionOptions{})
				require.NotEqual(t, "passed", result.Status)
				require.Contains(t, result.FailureReason, "extra")
			})
		}
	}
}
