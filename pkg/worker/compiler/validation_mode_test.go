package compiler

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/colony-2/c2j/pkg/cel"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/stretchr/testify/require"
)

type countingJobContext struct {
	jobKey swf.JobKey
	calls  int
}

func (c *countingJobContext) AwaitJobs(jobIds ...string) error {
	return nil
}

func (c *countingJobContext) GetJobKey() swf.JobKey            { return c.jobKey }
func (c *countingJobContext) Logger() *slog.Logger             { return slog.Default() }
func (c *countingJobContext) AwaitDuration(swf.Duration) error { return nil }
func (c *countingJobContext) DoTask(swf.RunPolicy, string, swf.TaskData) (swf.TaskData, error) {
	c.calls++
	return nil, fmt.Errorf("unexpected task invocation")
}

func withRegisteredOps(t *testing.T, opsToRegister ...coreops.RegisterableOp) {
	originalOps := coreops.List()
	coreops.Clear()
	if len(opsToRegister) > 0 {
		coreops.Register(opsToRegister...)
	}
	t.Cleanup(func() {
		coreops.Clear()
		if len(originalOps) > 0 {
			coreops.Register(originalOps...)
		}
	})
}

func registerOp(t *testing.T, opName string) {
	t.Helper()
	type input struct{}
	type output struct {
		Value string `json:"value"`
		Flag  bool   `json:"flag"`
	}

	op, err := coreops.NewOp().WithType(opName).AddStep(opName, coreops.NewStepWithDeps(
		func(_ coreops.OpDependencies, _ context.Context, _ input) (output, error) {
			return output{Value: "filled", Flag: true}, nil
		},
	)).Build()
	require.NoError(t, err)

	withRegisteredOps(t, op.(coreops.RegisterableOp))
}

func registerArtifactOp(t *testing.T, opName string) {
	t.Helper()
	type input struct{}
	type output struct {
		Value string `json:"value"`
		Flag  bool   `json:"flag"`
	}

	op, err := coreops.NewOp().
		WithType(opName).
		WithAcceptsArtifacts(true).
		AddStep(opName, coreops.NewStepWithDeps(
			func(_ coreops.OpDependencies, _ context.Context, _ input) (output, error) {
				return output{Value: "filled", Flag: true}, nil
			},
		)).Build()
	require.NoError(t, err)

	withRegisteredOps(t, op.(coreops.RegisterableOp))
}

func newWorkflowContext(jobCtx swf.JobContext) workflow.Context {
	return workflow.Context{
		JobContext:           jobCtx,
		ServiceDependencies2: coreops.NewServiceDepsBuilder().Build(),
	}
}

func mustCEL(t *testing.T, expr string) cel.CELExpr {
	t.Helper()
	out, err := cel.NewCELExpr(expr)
	require.NoError(t, err)
	return *out
}

func TestValidationAllowsNilInputsAndReturnsZeroOutputs(t *testing.T) {
	const opName = "validation-zero-op"
	registerOp(t, opName)

	jobCtx, gitCtx := GenerateTestContext()
	inner := &countingJobContext{jobKey: swf.JobKey{TenantId: "tenant", JobId: "job"}}
	ctx := newWorkflowContext(inner)

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeOp{
			RecipeMetadata: recipe.RecipeMetadata{
				InputSchema: map[string]recipe.InputSchema{
					"name": {Type: "string", Required: true},
					"flag": {Type: "boolean", Required: true},
				},
				NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
			},
			OpData: recipe.OpData{Op: opName},
		},
	}

	result, _, err := ExecuteRecipe(ctx, rec, nil, jobCtx, gitCtx, ExecutionOptions{Mode: ExecutionModeValidate})
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"value": "", "flag": false}, result)
	require.Equal(t, 0, inner.calls)
}

func TestValidationClampsRunIndex(t *testing.T) {
	const opName = "validation-run-clamp-op"
	registerOp(t, opName)

	jobCtx, gitCtx := GenerateTestContext()
	ctx := newWorkflowContext(&countingJobContext{})

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeSequence{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
			},
			SequenceData: recipe.SequenceData{
				Sequence: []recipe.Node{
					{NodeImpl: &recipe.NodeOp{
						NodeMetadata: recipe.NodeMetadata{ID: "call", Inputs: map[string]interface{}{}},
						OpData:       recipe.OpData{Op: opName},
					}},
				},
				Outputs: map[string]interface{}{
					"value": "${{ sequence.call.runs[3].outputs.value }}",
					"flag":  "{{ sequence.call.outputs.flag }}",
				},
			},
		},
	}

	result, _, err := ExecuteRecipe(ctx, rec, map[string]interface{}{}, jobCtx, gitCtx, ExecutionOptions{Mode: ExecutionModeValidate})
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"value": "", "flag": false}, result)
}

func TestValidationAllowsArtifactBindingsToFutureArtifactNames(t *testing.T) {
	const opName = "validation-artifact-binding-op"
	registerArtifactOp(t, opName)

	jobCtx, gitCtx := GenerateTestContext()
	ctx := newWorkflowContext(&countingJobContext{})

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeSequence{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
			},
			SequenceData: recipe.SequenceData{
				Sequence: []recipe.Node{
					{NodeImpl: &recipe.NodeOp{
						NodeMetadata: recipe.NodeMetadata{ID: "write", Inputs: map[string]interface{}{}},
						OpData:       recipe.OpData{Op: opName},
					}},
					{NodeImpl: &recipe.NodeOp{
						NodeMetadata: recipe.NodeMetadata{
							ID:     "read",
							Inputs: map[string]interface{}{},
							Artifacts: map[string]interface{}{
								"foo.txt": `${{ sequence.write.artifacts["foo.txt"] }}`,
							},
						},
						OpData: recipe.OpData{Op: opName},
					}},
				},
				Outputs: map[string]interface{}{
					"result": "${{ sequence.read.outputs.value }}",
				},
			},
		},
	}

	result, _, err := ExecuteRecipe(ctx, rec, map[string]interface{}{}, jobCtx, gitCtx, ExecutionOptions{Mode: ExecutionModeValidate})
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"result": ""}, result)
}

func TestValidationAllowsFutureStateReference(t *testing.T) {
	const opName = "validation-future-state-op"
	registerOp(t, opName)

	jobCtx, gitCtx := GenerateTestContext()
	ctx := newWorkflowContext(&countingJobContext{})

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeState{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
			},
			StateMachineData: recipe.StateMachineData{
				Outputs: map[string]interface{}{
					"result":  "{{ states.b.outputs.value }}",
					"attempt": "${{ states.a.runs[2].outputs.value }}",
				},
				States: &recipe.StateMap{
					Initial: recipe.InitialState("a"),
					States: map[string]recipe.State{
						"a": {
							Node: recipe.Node{NodeImpl: &recipe.NodeOp{
								NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
								OpData:       recipe.OpData{Op: opName},
							}},
							SingleStateMetadata: recipe.SingleStateMetadata{
								Transitions: []recipe.Transition{{To: "b", When: mustCEL(t, "true")}},
							},
						},
						"b": {
							Node: recipe.Node{NodeImpl: &recipe.NodeOp{
								NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
								OpData:       recipe.OpData{Op: opName},
							}},
						},
					},
				},
			},
		},
	}

	_, _, err := ExecuteRecipe(ctx, rec, map[string]interface{}{}, jobCtx, gitCtx, ExecutionOptions{Mode: ExecutionModeValidate})
	require.NoError(t, err)
}

func TestValidateAllCatchesLaterStateErrors(t *testing.T) {
	const opName = "validation-later-state-op"
	registerOp(t, opName)

	jobCtx, gitCtx := GenerateTestContext()
	ctx := newWorkflowContext(&countingJobContext{})

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeState{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
			},
			StateMachineData: recipe.StateMachineData{
				Outputs: map[string]interface{}{
					"result": "{{ states.a.outputs.value }}",
				},
				States: &recipe.StateMap{
					Initial: recipe.InitialState("a"),
					States: map[string]recipe.State{
						"a": {
							Node: recipe.Node{NodeImpl: &recipe.NodeOp{
								NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
								OpData:       recipe.OpData{Op: opName},
							}},
							SingleStateMetadata: recipe.SingleStateMetadata{
								Transitions: []recipe.Transition{{To: "done", When: mustCEL(t, "true")}},
							},
						},
						"done": {
							Node: recipe.Node{NodeImpl: &recipe.NodeOp{
								NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
								OpData:       recipe.OpData{Op: opName},
							}},
						},
						"later": {
							Node: recipe.Node{NodeImpl: &recipe.NodeOp{
								NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{
									"bad": "{{ bananas.missing }}",
								}},
								OpData: recipe.OpData{Op: opName},
							}},
						},
					},
				},
			},
		},
	}

	_, _, err := ExecuteRecipe(ctx, rec, map[string]interface{}{}, jobCtx, gitCtx, ExecutionOptions{
		Mode:       ExecutionModeValidate,
		Validation: ValidationOptions{Mode: ValidateAll},
	})
	require.Error(t, err)

	_, _, err = ExecuteRecipe(ctx, rec, map[string]interface{}{}, jobCtx, gitCtx, ExecutionOptions{
		Mode:       ExecutionModeValidate,
		Validation: ValidationOptions{Mode: ValidatePathOnly},
	})
	require.NoError(t, err)
}

func TestValidationRejectsInvalidCEL(t *testing.T) {
	const opName = "validation-invalid-cel-op"
	registerOp(t, opName)

	jobCtx, gitCtx := GenerateTestContext()
	ctx := newWorkflowContext(&countingJobContext{})

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeSequence{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
			},
			SequenceData: recipe.SequenceData{
				Sequence: []recipe.Node{
					{NodeImpl: &recipe.NodeOp{
						NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
						OpData:       recipe.OpData{Op: opName},
					}},
				},
				Outputs: map[string]interface{}{
					"bad": "${{ inputs.foo + }}",
				},
			},
		},
	}

	_, _, err := ExecuteRecipe(ctx, rec, map[string]interface{}{}, jobCtx, gitCtx, ExecutionOptions{Mode: ExecutionModeValidate})
	require.Error(t, err)
}
