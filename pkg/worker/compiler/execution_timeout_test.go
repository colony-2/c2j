package compiler

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/ops/sleepop"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type policyCaptureJobContext struct {
	noLeaseClientContext
	jobKey   jobdb.JobKey
	out      jobdb.TaskData
	sleep    time.Duration
	calls    int
	policies []jobdb.RunPolicy
	scopes   []testScopeOptions
}

func (c *policyCaptureJobContext) AwaitJobs(jobIds ...string) error {
	return nil
}

func (c *policyCaptureJobContext) GetJobKey() jobdb.JobKey            { return c.jobKey }
func (c *policyCaptureJobContext) Logger() *slog.Logger               { return slog.Default() }
func (c *policyCaptureJobContext) AwaitDuration(jobdb.Duration) error { return nil }

func (c *policyCaptureJobContext) DoTask(call testTaskCall, build func(testContext) (jobdb.TaskData, error)) (jobdb.TaskData, error) {
	data, buildErr := build(c.Context())
	if buildErr != nil {
		return nil, buildErr
	}
	_ = data
	policy := call.Policy
	_ = policy
	taskType := call.TaskType
	_ = taskType
	c.calls++
	c.policies = append(c.policies, policy)
	if c.sleep > 0 {
		time.Sleep(c.sleep)
		if policy.TotalTimeout != nil && c.sleep >= time.Duration(*policy.TotalTimeout) {
			return nil, context.DeadlineExceeded
		}
	}
	return c.out, nil
}

func newTimeoutTestOp(t *testing.T, opType string, defaultTimeout time.Duration) coreops.RegisterableOp {
	t.Helper()
	type output struct {
		OK bool `json:"ok"`
	}
	builder := coreops.NewOp().
		WithType(opType).
		AddStep("run", coreops.NewStepWithDeps(func(_ coreops.OpDependencies, _ context.Context, in map[string]interface{}) (output, error) {
			return output{OK: true}, nil
		}))
	if defaultTimeout > 0 {
		builder = builder.WithDefaultTimeout(defaultTimeout)
	}
	op, err := builder.Build()
	require.NoError(t, err)
	return op.(coreops.RegisterableOp)
}

func newTimeoutWorkflow(t *testing.T, gitCtx contextual.GitCommitContext, sleep time.Duration) (*policyCaptureJobContext, workflow.Context) {
	t.Helper()
	stub := &policyCaptureJobContext{
		jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "timeout-test"},
		out:    newActivityOutputTaskData(t, gitCtx),
		sleep:  sleep,
	}
	return stub, newWorkflowContext(stub)
}

func requireCapturedScopeTimeout(t *testing.T, stub *policyCaptureJobContext, want time.Duration) {
	t.Helper()
	var found bool
	for _, scope := range stub.scopes {
		if scope.TotalTimeout == want {
			found = true
		}
	}
	require.True(t, found, "scope budget %s absent from %+v", want, stub.scopes)
	for _, policy := range stub.policies {
		require.Nil(t, policy.TotalTimeout, "scope enforces the shared budget")
	}
}

func TestBuiltInOpsOnlyUseExplicitTimeouts(t *testing.T) {
	registered := []coreops.RegisterableOp{commandop.GetOp(), sleepop.GetOp(), extensions.GetExecutionOp()}
	for _, op := range registered {
		require.Zero(t, op.GetMetadata().DefaultTimeout, op.GetName())
	}
	withRegisteredOps(t, registered...)
	for _, tc := range []struct {
		op     string
		inputs map[string]interface{}
	}{
		{"command_execution", map[string]interface{}{"run": "true", "timeout": "10m"}},
		{"sleep", map[string]interface{}{"duration": "48h"}},
		{"extension_execution", map[string]interface{}{"selector": "./probe"}},
		{"nix:github:example/ops/main#probe", nil},
	} {
		for _, timeout := range []time.Duration{0, time.Hour} {
			t.Run(tc.op+"/"+timeout.String(), func(t *testing.T) {
				jobCtx, gitCtx := GenerateTestContext()
				stub, wfCtx := newTimeoutWorkflow(t, gitCtx, 0)
				rec := recipe.Recipe{RecipeImpl: &recipe.RecipeOp{
					RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{
						ID: "root", Inputs: tc.inputs, Timeout: recipe.Duration(timeout),
					}},
					OpData: recipe.OpData{Op: tc.op},
				}}
				_, _, err := ExecuteRecipe(wfCtx, rec, map[string]interface{}{}, jobCtx, gitCtx)
				require.NoError(t, err)
				require.Equal(t, 1, stub.calls)
				require.NotEmpty(t, stub.scopes)
				for _, scope := range stub.scopes {
					require.Equal(t, timeout, scope.TotalTimeout)
					require.Zero(t, scope.AttemptTimeout)
				}
				for _, policy := range stub.policies {
					require.Nil(t, policy.TotalTimeout)
					require.Nil(t, policy.InvocationTimeout)
				}
			})
		}
	}
}

func TestExecuteOpUsesOpDefaultTimeoutWhenNodeTimeoutMissing(t *testing.T) {
	opType := "timeout-default-op"
	withRegisteredOps(t, newTimeoutTestOp(t, opType, 2*time.Minute))
	jobCtx, gitCtx := GenerateTestContext()
	stub, wfCtx := newTimeoutWorkflow(t, gitCtx, 0)

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeOp{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{ID: "root", Inputs: map[string]interface{}{}},
			},
			OpData: recipe.OpData{Op: opType},
		},
	}

	_, _, err := ExecuteRecipe(wfCtx, rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, 1, stub.calls)
	requireCapturedScopeTimeout(t, stub, 2*time.Minute)
}

func TestExecuteOpExplicitTimeoutOverridesOpDefault(t *testing.T) {
	opType := "timeout-explicit-op"
	withRegisteredOps(t, newTimeoutTestOp(t, opType, 2*time.Minute))
	jobCtx, gitCtx := GenerateTestContext()
	stub, wfCtx := newTimeoutWorkflow(t, gitCtx, 0)

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeOp{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{
					ID:      "root",
					Timeout: recipe.Duration(5 * time.Second),
					Inputs:  map[string]interface{}{},
				},
			},
			OpData: recipe.OpData{Op: opType},
		},
	}

	_, _, err := ExecuteRecipe(wfCtx, rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, 1, stub.calls)
	requireCapturedScopeTimeout(t, stub, 5*time.Second)
}

func TestSequenceTimeoutClampsNestedOpTimeout(t *testing.T) {
	opType := "timeout-sequence-clamp-op"
	withRegisteredOps(t, newTimeoutTestOp(t, opType, 2*time.Minute))
	jobCtx, gitCtx := GenerateTestContext()
	stub, wfCtx := newTimeoutWorkflow(t, gitCtx, 0)

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeSequence{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{
					ID:      "root-sequence",
					Timeout: recipe.Duration(50 * time.Millisecond),
				},
			},
			SequenceData: recipe.SequenceData{Sequence: []recipe.Node{{
				NodeImpl: &recipe.NodeOp{
					NodeMetadata: recipe.NodeMetadata{ID: "slow", Inputs: map[string]interface{}{}},
					OpData:       recipe.OpData{Op: opType},
				},
			}}},
		},
	}

	_, _, err := ExecuteRecipe(wfCtx, rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, 1, stub.calls)
	requireCapturedScopeTimeout(t, stub, 50*time.Millisecond)
}

func TestNestedOpTimeoutClampsBelowParentSequenceTimeout(t *testing.T) {
	opType := "timeout-child-clamp-op"
	withRegisteredOps(t, newTimeoutTestOp(t, opType, 2*time.Minute))
	jobCtx, gitCtx := GenerateTestContext()
	stub, wfCtx := newTimeoutWorkflow(t, gitCtx, 0)

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeSequence{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{
					ID:      "root-sequence",
					Timeout: recipe.Duration(time.Minute),
				},
			},
			SequenceData: recipe.SequenceData{Sequence: []recipe.Node{{
				NodeImpl: &recipe.NodeOp{
					NodeMetadata: recipe.NodeMetadata{
						ID:      "child",
						Timeout: recipe.Duration(20 * time.Millisecond),
						Inputs:  map[string]interface{}{},
					},
					OpData: recipe.OpData{Op: opType},
				},
			}}},
		},
	}

	_, _, err := ExecuteRecipe(wfCtx, rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, 1, stub.calls)
	requireCapturedScopeTimeout(t, stub, 20*time.Millisecond)
}

func TestStateMachineTimeoutClampsNestedOpTimeout(t *testing.T) {
	opType := "timeout-state-machine-clamp-op"
	withRegisteredOps(t, newTimeoutTestOp(t, opType, 2*time.Minute))
	jobCtx, gitCtx := GenerateTestContext()
	stub, wfCtx := newTimeoutWorkflow(t, gitCtx, 0)

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeState{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{
					ID:      "root-state-machine",
					Timeout: recipe.Duration(50 * time.Millisecond),
				},
			},
			StateMachineData: recipe.StateMachineData{States: &recipe.StateMap{
				Initial: recipe.InitialState("start"),
				States: map[string]recipe.State{
					"start": {
						Node: recipe.Node{NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{ID: "state-op", Inputs: map[string]interface{}{}},
							OpData:       recipe.OpData{Op: opType},
						}},
					},
				},
			}},
		},
	}

	_, _, err := ExecuteRecipe(wfCtx, rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, 1, stub.calls)
	requireCapturedScopeTimeout(t, stub, 50*time.Millisecond)
}

func (c *policyCaptureJobContext) DoScope(site string, opts testScopeOptions, body func(testExecutionContext) (jobdb.JobData, error)) (jobdb.JobData, error) {
	c.scopes = append(c.scopes, opts)
	return body(c)
}
func (c *policyCaptureJobContext) Context() testContext { return testBackground() }
func (c *policyCaptureJobContext) ExecutionVersions() jobdb.ExecutionVersions {
	return jobdb.ExecutionVersions{}
}

func (c *policyCaptureJobContext) Job() jobdb.JobHandle {
	return jobdb.JobHandle{JobKey: c.GetJobKey()}
}
