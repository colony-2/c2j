package compiler

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	recipecel "github.com/colony-2/c2j/pkg/cel"
	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type catchScriptedJobContext struct {
	jobKey   jobdb.JobKey
	results  []catchTaskResult
	calls    int
	waits    []jobdb.Duration
	policies []jobdb.RunPolicy
}

type catchTaskResult struct {
	out jobdb.TaskData
	err error
}

func (c *catchScriptedJobContext) AwaitJobs(jobIds ...string) error { return nil }
func (c *catchScriptedJobContext) GetJobKey() jobdb.JobKey          { return c.jobKey }
func (c *catchScriptedJobContext) Logger() *slog.Logger             { return slog.Default() }
func (c *catchScriptedJobContext) AwaitDuration(d jobdb.Duration) error {
	c.waits = append(c.waits, d)
	return nil
}

func (c *catchScriptedJobContext) DoTask(policy jobdb.RunPolicy, _ string, _ jobdb.TaskData) (jobdb.TaskData, error) {
	c.policies = append(c.policies, policy)
	idx := c.calls
	c.calls++
	if idx >= len(c.results) {
		return nil, fmt.Errorf("unexpected task invocation %d", idx)
	}
	return c.results[idx].out, c.results[idx].err
}

func registerCatchOp(t *testing.T, opName string) {
	t.Helper()
	op, err := coreops.NewOp().
		WithType(opName).
		AddStep("run", coreops.NewStepWithDeps(func(_ coreops.OpDependencies, _ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{"ok": true}, nil
		})).
		Build()
	require.NoError(t, err)
	withRegisteredOps(t, op.(coreops.RegisterableOp))
}

func catchExpr(t *testing.T, expr string) recipecel.CELExpr {
	t.Helper()
	var out recipecel.CELExpr
	err := out.UnmarshalYAML(func(target interface{}) error {
		str, ok := target.(*string)
		if !ok {
			return fmt.Errorf("expected *string")
		}
		*str = expr
		return nil
	})
	require.NoError(t, err)
	return out
}

func catchOutput(t *testing.T, gitCtx contextual.GitCommitContext, output map[string]interface{}) jobdb.TaskData {
	t.Helper()
	envelope := workerops.ActivityInvocationOutput{
		OpOutput:  output,
		GitResult: gitCtx,
	}
	outEnv, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, envelope)
	require.NoError(t, err)
	return jobdb.NewTaskDataOrPanic(outEnv)
}

func appFailure(message string, code string) error {
	return &jobdb.AppError{Payload: jobdb.AppErrorPayload{
		Message: message,
		Attrs:   map[string]interface{}{"code": code},
	}}
}

func catchWorkflowContext(job jobworkflow.JobContext) workflow.Context {
	return workflow.Context{
		JobContext:           job,
		ServiceDependencies2: coreops.NewServiceDepsBuilder().Build(),
	}
}

func TestStateCatchRoutesTaskErrorWithFailureTransition(t *testing.T) {
	const opName = "catch-state-route-op"
	registerCatchOp(t, opName)
	jobCtx, gitCtx := GenerateTestContext()
	script := &catchScriptedJobContext{
		jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "catch-route"},
		results: []catchTaskResult{
			{err: appFailure("compile failed", "E_BAD")},
			{out: catchOutput(t, gitCtx, map[string]interface{}{"ok": true, "seen": "review"})},
		},
	}

	rec := recipe.Recipe{RecipeImpl: &recipe.RecipeState{
		RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}}},
		StateMachineData: recipe.StateMachineData{
			Outputs: map[string]interface{}{
				"handled": "${{ states.review.outputs.ok }}",
			},
			States: &recipe.StateMap{
				Initial: recipe.InitialState("work"),
				States: map[string]recipe.State{
					"work": {
						Node: recipe.Node{NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{
								Inputs: map[string]interface{}{},
								Catch: []recipe.CatchClause{{
									ID:      "bad_to_review",
									When:    catchExpr(t, `failure.kind == "task_error" && failure.code == "E_BAD"`),
									To:      "review",
									Payload: map[string]interface{}{"message": "${{ failure.message }}"},
								}},
							},
							OpData: recipe.OpData{Op: opName},
						}},
					},
					"review": {
						Node: recipe.Node{NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{
								"kind":    "${{ transition.failure.kind }}",
								"message": "${{ transition.payload.message }}",
							}},
							OpData: recipe.OpData{Op: opName},
						}},
					},
				},
			},
		},
	}}

	result, _, err := ExecuteRecipe(catchWorkflowContext(script), rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"handled": true}, result)
	require.Equal(t, 2, script.calls)
	require.Equal(t, int32(1), script.policies[0].Retry.MaximumAttempts)
}

func TestStateMachineFallbackCatchHandlesUnmatchedStateCatch(t *testing.T) {
	const opName = "catch-state-machine-fallback-op"
	registerCatchOp(t, opName)
	jobCtx, gitCtx := GenerateTestContext()
	script := &catchScriptedJobContext{
		jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "catch-fallback"},
		results: []catchTaskResult{
			{err: appFailure("runtime failed", "E_OTHER")},
			{out: catchOutput(t, gitCtx, map[string]interface{}{"ok": true})},
		},
	}

	rec := recipe.Recipe{RecipeImpl: &recipe.RecipeState{
		RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{
			Inputs: map[string]interface{}{},
			Catch: []recipe.CatchClause{{
				ID:   "fallback",
				When: catchExpr(t, `failure.kind == "task_error"`),
				To:   "fallback",
			}},
		}},
		StateMachineData: recipe.StateMachineData{
			Outputs: map[string]interface{}{"fallback": "${{ states.fallback.outputs.ok }}"},
			States: &recipe.StateMap{
				Initial: recipe.InitialState("work"),
				States: map[string]recipe.State{
					"work": {
						Node: recipe.Node{NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{
								Inputs: map[string]interface{}{},
								Catch: []recipe.CatchClause{{
									When: catchExpr(t, `failure.code == "E_BAD"`),
									To:   "never",
								}},
							},
							OpData: recipe.OpData{Op: opName},
						}},
					},
					"fallback": {
						Node: recipe.Node{NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
							OpData:       recipe.OpData{Op: opName},
						}},
					},
					"never": {
						Node: recipe.Node{NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
							OpData:       recipe.OpData{Op: opName},
						}},
					},
				},
			},
		},
	}}

	result, _, err := ExecuteRecipe(catchWorkflowContext(script), rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"fallback": true}, result)
	require.Equal(t, 2, script.calls)
}

func TestStateCatchContinueOutputsDriveNormalTransition(t *testing.T) {
	const opName = "catch-state-continue-op"
	registerCatchOp(t, opName)
	jobCtx, gitCtx := GenerateTestContext()
	script := &catchScriptedJobContext{
		jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "catch-continue"},
		results: []catchTaskResult{
			{err: appFailure("known failure", "E_BAD")},
			{out: catchOutput(t, gitCtx, map[string]interface{}{"ok": true})},
		},
	}

	rec := recipe.Recipe{RecipeImpl: &recipe.RecipeState{
		RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}}},
		StateMachineData: recipe.StateMachineData{
			Outputs: map[string]interface{}{"done": "${{ states.done.outputs.ok }}"},
			States: &recipe.StateMap{
				Initial: recipe.InitialState("work"),
				States: map[string]recipe.State{
					"work": {
						Node: recipe.Node{NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{
								Inputs: map[string]interface{}{},
								Catch: []recipe.CatchClause{{
									When: catchExpr(t, `failure_has_code(failure, "E_BAD")`),
									Continue: &recipe.CatchContinue{Outputs: map[string]interface{}{
										"status": "recovered",
									}},
								}},
							},
							OpData: recipe.OpData{Op: opName},
						}},
						SingleStateMetadata: recipe.SingleStateMetadata{Transitions: []recipe.Transition{{
							To:   "done",
							When: catchExpr(t, `outputs.status == "recovered"`),
						}}},
					},
					"done": {
						Node: recipe.Node{NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
							OpData:       recipe.OpData{Op: opName},
						}},
					},
				},
			},
		},
	}}

	result, _, err := ExecuteRecipe(catchWorkflowContext(script), rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"done": true}, result)
	require.Equal(t, 2, script.calls)
}

func TestCatchFailRewritesFailureForParentCatch(t *testing.T) {
	const opName = "catch-fail-rewrite-op"
	registerCatchOp(t, opName)
	jobCtx, gitCtx := GenerateTestContext()
	script := &catchScriptedJobContext{
		jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "catch-fail"},
		results: []catchTaskResult{
			{err: appFailure("low level", "E_BAD")},
			{out: catchOutput(t, gitCtx, map[string]interface{}{"ok": true})},
		},
	}

	rec := recipe.Recipe{RecipeImpl: &recipe.RecipeState{
		RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{
			Inputs: map[string]interface{}{},
			Catch: []recipe.CatchClause{{
				When: catchExpr(t, `failure.code == "REWRITTEN" && failure.cause.code == "E_BAD"`),
				To:   "handled",
			}},
		}},
		StateMachineData: recipe.StateMachineData{
			Outputs: map[string]interface{}{"handled": "${{ states.handled.outputs.ok }}"},
			States: &recipe.StateMap{
				Initial: recipe.InitialState("work"),
				States: map[string]recipe.State{
					"work": {
						Node: recipe.Node{NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{
								Inputs: map[string]interface{}{},
								Catch: []recipe.CatchClause{{
									When: catchExpr(t, `true`),
									Fail: &recipe.CatchFail{
										Kind:    string(recipe.FailureKindTaskError),
										Code:    "REWRITTEN",
										Message: "rewritten failure",
									},
								}},
							},
							OpData: recipe.OpData{Op: opName},
						}},
					},
					"handled": {
						Node: recipe.Node{NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
							OpData:       recipe.OpData{Op: opName},
						}},
					},
				},
			},
		},
	}}

	result, _, err := ExecuteRecipe(catchWorkflowContext(script), rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"handled": true}, result)
	require.Equal(t, 2, script.calls)
}

func TestRootOpCatchContinueSuppressesRetry(t *testing.T) {
	const opName = "catch-root-continue-op"
	registerCatchOp(t, opName)
	jobCtx, gitCtx := GenerateTestContext()
	script := &catchScriptedJobContext{
		jobKey:  jobdb.JobKey{TenantId: "tenant", JobId: "catch-root"},
		results: []catchTaskResult{{err: appFailure("known", "E_BAD")}},
	}
	retry := recipe.RetryPolicy{MaximumAttempts: 3, InitialInterval: jobdb.Duration(10 * time.Millisecond)}
	rec := recipe.Recipe{RecipeImpl: &recipe.RecipeOp{
		RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{
			Inputs: map[string]interface{}{},
			Retry:  &retry,
			Catch: []recipe.CatchClause{{
				When: catchExpr(t, `failure.code == "E_BAD"`),
				Continue: &recipe.CatchContinue{Outputs: map[string]interface{}{
					"ok":      true,
					"message": "${{ failure.message }}",
				}},
			}},
		}},
		OpData: recipe.OpData{Op: opName},
	}}

	result, _, err := ExecuteRecipe(catchWorkflowContext(script), rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"ok": true, "message": "known"}, result)
	require.Equal(t, 1, script.calls)
	require.Equal(t, int32(1), script.policies[0].Retry.MaximumAttempts)
	require.Empty(t, script.waits)
}

func TestUnhandledLocalCatchRetriesAccordingToPolicy(t *testing.T) {
	const opName = "catch-unhandled-retry-op"
	registerCatchOp(t, opName)
	jobCtx, gitCtx := GenerateTestContext()
	script := &catchScriptedJobContext{
		jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "catch-retry"},
		results: []catchTaskResult{
			{err: appFailure("first", "E_OTHER")},
			{err: appFailure("second", "E_OTHER")},
			{out: catchOutput(t, gitCtx, map[string]interface{}{"ok": true})},
		},
	}
	retry := recipe.RetryPolicy{MaximumAttempts: 3, InitialInterval: jobdb.Duration(5 * time.Millisecond)}
	rec := recipe.Recipe{RecipeImpl: &recipe.RecipeOp{
		RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{
			Inputs: map[string]interface{}{},
			Retry:  &retry,
			Catch: []recipe.CatchClause{{
				When: catchExpr(t, `failure.code == "E_BAD"`),
				Continue: &recipe.CatchContinue{Outputs: map[string]interface{}{
					"ok": true,
				}},
			}},
		}},
		OpData: recipe.OpData{Op: opName},
	}}

	result, _, err := ExecuteRecipe(catchWorkflowContext(script), rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"ok": true}, result)
	require.Equal(t, 3, script.calls)
	require.Len(t, script.waits, 2)
}

func TestStateCatchNoMatchRetriesStateOpBeforeFailing(t *testing.T) {
	const opName = "catch-state-unmatched-retry-op"
	registerCatchOp(t, opName)
	jobCtx, gitCtx := GenerateTestContext()
	script := &catchScriptedJobContext{
		jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "catch-state-retry"},
		results: []catchTaskResult{
			{err: appFailure("first", "E_OTHER")},
			{out: catchOutput(t, gitCtx, map[string]interface{}{"ok": true})},
		},
	}
	retry := recipe.RetryPolicy{MaximumAttempts: 2, InitialInterval: jobdb.Duration(5 * time.Millisecond)}
	rec := recipe.Recipe{RecipeImpl: &recipe.RecipeState{
		RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}}},
		StateMachineData: recipe.StateMachineData{
			Outputs: map[string]interface{}{"ok": "${{ states.work.outputs.ok }}"},
			States: &recipe.StateMap{
				Initial: recipe.InitialState("work"),
				States: map[string]recipe.State{
					"work": {
						Node: recipe.Node{NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{
								Inputs: map[string]interface{}{},
								Retry:  &retry,
								Catch: []recipe.CatchClause{{
									When: catchExpr(t, `failure.code == "E_BAD"`),
									Continue: &recipe.CatchContinue{Outputs: map[string]interface{}{
										"ok": false,
									}},
								}},
							},
							OpData: recipe.OpData{Op: opName},
						}},
					},
				},
			},
		},
	}}

	result, _, err := ExecuteRecipe(catchWorkflowContext(script), rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"ok": true}, result)
	require.Equal(t, 2, script.calls)
	require.Len(t, script.waits, 1)
	require.Equal(t, int32(1), script.policies[0].Retry.MaximumAttempts)
	require.Equal(t, int32(1), script.policies[1].Retry.MaximumAttempts)
}

func TestStateMachineCatchNoMatchRetriesStateOpBeforeFailing(t *testing.T) {
	const opName = "catch-machine-unmatched-retry-op"
	registerCatchOp(t, opName)
	jobCtx, gitCtx := GenerateTestContext()
	script := &catchScriptedJobContext{
		jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "catch-machine-retry"},
		results: []catchTaskResult{
			{err: appFailure("first", "E_OTHER")},
			{out: catchOutput(t, gitCtx, map[string]interface{}{"ok": true})},
		},
	}
	retry := recipe.RetryPolicy{MaximumAttempts: 2, InitialInterval: jobdb.Duration(5 * time.Millisecond)}
	rec := recipe.Recipe{RecipeImpl: &recipe.RecipeState{
		RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{
			Inputs: map[string]interface{}{},
			Catch: []recipe.CatchClause{{
				When: catchExpr(t, `failure.code == "E_BAD"`),
				To:   "fallback",
			}},
		}},
		StateMachineData: recipe.StateMachineData{
			Outputs: map[string]interface{}{"ok": "${{ states.work.outputs.ok }}"},
			States: &recipe.StateMap{
				Initial: recipe.InitialState("work"),
				States: map[string]recipe.State{
					"work": {
						Node: recipe.Node{NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{
								Inputs: map[string]interface{}{},
								Retry:  &retry,
							},
							OpData: recipe.OpData{Op: opName},
						}},
					},
					"fallback": {
						Node: recipe.Node{NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}},
							OpData:       recipe.OpData{Op: opName},
						}},
					},
				},
			},
		},
	}}

	result, _, err := ExecuteRecipe(catchWorkflowContext(script), rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"ok": true}, result)
	require.Equal(t, 2, script.calls)
	require.Len(t, script.waits, 1)
	require.Equal(t, int32(1), script.policies[0].Retry.MaximumAttempts)
	require.Equal(t, int32(1), script.policies[1].Retry.MaximumAttempts)
}

func TestSequenceChildCatchContinueFeedsLaterSibling(t *testing.T) {
	const opName = "catch-sequence-child-op"
	registerCatchOp(t, opName)
	jobCtx, gitCtx := GenerateTestContext()
	script := &catchScriptedJobContext{
		jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "catch-sequence"},
		results: []catchTaskResult{
			{err: appFailure("child failed", "E_BAD")},
			{out: catchOutput(t, gitCtx, map[string]interface{}{"ok": true})},
		},
	}
	rec := recipe.Recipe{RecipeImpl: &recipe.RecipeSequence{
		RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{Inputs: map[string]interface{}{}}},
		SequenceData: recipe.SequenceData{
			Sequence: []recipe.Node{
				{NodeImpl: &recipe.NodeOp{
					NodeMetadata: recipe.NodeMetadata{
						ID:     "first",
						Inputs: map[string]interface{}{},
						Catch: []recipe.CatchClause{{
							When: catchExpr(t, `failure.code == "E_BAD"`),
							Continue: &recipe.CatchContinue{Outputs: map[string]interface{}{
								"ok": true,
							}},
						}},
					},
					OpData: recipe.OpData{Op: opName},
				}},
				{NodeImpl: &recipe.NodeOp{
					NodeMetadata: recipe.NodeMetadata{
						ID: "second",
						Inputs: map[string]interface{}{
							"from_first": "${{ sequence.first.outputs.ok }}",
						},
					},
					OpData: recipe.OpData{Op: opName},
				}},
			},
			Outputs: map[string]interface{}{
				"first":  "${{ sequence.first.outputs.ok }}",
				"second": "${{ sequence.second.outputs.ok }}",
			},
		},
	}}

	result, _, err := ExecuteRecipe(catchWorkflowContext(script), rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"first": true, "second": true}, result)
	require.Equal(t, 2, script.calls)
}
