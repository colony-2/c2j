package template

import (
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template/funcregistry"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/stretchr/testify/require"
)

// spyProvider adds a function that would panic if actually executed.
type spyProvider struct {
	executed *bool
}

func (s spyProvider) TypeOptions() []cel.EnvOption {
	return nil
}

func (s spyProvider) FunctionOptions(adapter types.Adapter) ([]cel.EnvOption, error) {
	return []cel.EnvOption{
		cel.Function(
			"explode",
			cel.Overload(
				"explode_zero",
				[]*cel.Type{},
				cel.StringType,
				cel.FunctionBinding(func(values ...ref.Val) ref.Val {
					*s.executed = true
					return types.String("boom")
				}),
			),
		),
	}, nil
}

func (s spyProvider) FunctionOptionsWithContext(adapter types.Adapter, _ funcregistry.ContextProvider) ([]cel.EnvOption, error) {
	return s.FunctionOptions(adapter)
}

func TestValidateModeDoesNotExecuteFunctions(t *testing.T) {
	ran := false
	opts := DefaultResolutionOptions()
	opts.Mode = ModeValidate
	opts.CELOptionsProvider = spyProvider{executed: &ran}

	ctx, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{}, opts)
	require.NoError(t, err)

	// Expression that would call the custom function if evaluated.
	result, err := ctx.evaluateCELExpression("explode()")
	require.NoError(t, err)
	require.False(t, ran, "function bindings should not execute in validate mode")

	// Should return placeholder compatible with declared return type (string -> empty string).
	require.Equal(t, "", result)
}

func TestValidateModeResolvesSafeStateHelperFallbacks(t *testing.T) {
	opts := DefaultResolutionOptions()
	opts.Mode = ModeValidate

	ctx, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{}, opts)
	require.NoError(t, err)
	smCtx, err := ctx.NewChildContext(ScopeStateMachine, recipe.NodeMetadata{ID: "sm"}, "", map[string]interface{}{})
	require.NoError(t, err)

	placeholderOutputs := map[string]interface{}{"sessionId": "future-session"}
	smCtx.TemplateData.States["implement"] = StepOutput{
		Outputs: placeholderOutputs,
		Runs: []RunOutput{
			{Outputs: placeholderOutputs},
		},
	}

	result, err := smCtx.resolveValue(map[string]interface{}{
		"sessionId": `${{ state_exists("implement") ? state_output("implement", "sessionId", "") : "" }}`,
	})
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"sessionId": ""}, result)
}

func TestValidateModeResolvesOptionalAndNonemptyFallbacks(t *testing.T) {
	opts := DefaultResolutionOptions()
	opts.Mode = ModeValidate

	ctx, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, map[string]interface{}{
		"blank": "  ",
	}, contextual.JobContext{}, opts)
	require.NoError(t, err)

	result, err := ctx.resolveValue(map[string]interface{}{
		"optional": `${{ inputs.?missing.orValue("") }}`,
		"first":    `${{ first_nonempty(inputs.?missing, inputs.blank, "fallback") }}`,
	})
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{
		"optional": "",
		"first":    "fallback",
	}, result)
}

func TestValidateModeSafeStateHelpersDoNotExecuteUnsafeFallbacks(t *testing.T) {
	ran := false
	opts := DefaultResolutionOptions()
	opts.Mode = ModeValidate
	opts.CELOptionsProvider = spyProvider{executed: &ran}

	ctx, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{}, opts)
	require.NoError(t, err)

	result, err := ctx.evaluateCELExpression(`state_output("missing", "sessionId", explode())`)
	require.NoError(t, err)
	require.False(t, ran, "unsafe calls nested inside safe helpers should not execute in validate mode")
	require.Nil(t, result)
}
