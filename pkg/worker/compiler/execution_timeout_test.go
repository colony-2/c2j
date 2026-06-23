package compiler

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type policyCaptureJobContext struct {
	jobKey   jobdb.JobKey
	out      jobdb.TaskData
	sleep    time.Duration
	calls    int
	policies []jobdb.RunPolicy
}

func (c *policyCaptureJobContext) AwaitJobs(jobIds ...string) error {
	return nil
}

func (c *policyCaptureJobContext) GetJobKey() jobdb.JobKey            { return c.jobKey }
func (c *policyCaptureJobContext) Logger() *slog.Logger               { return slog.Default() }
func (c *policyCaptureJobContext) AwaitDuration(jobdb.Duration) error { return nil }

func (c *policyCaptureJobContext) DoTask(policy jobdb.RunPolicy, taskType string, data jobdb.TaskData) (jobdb.TaskData, error) {
	c.calls++
	c.policies = append(c.policies, policy)
	if c.sleep > 0 {
		time.Sleep(c.sleep)
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

func requireCapturedTimeoutBetween(t *testing.T, policy jobdb.RunPolicy, min time.Duration, max time.Duration) {
	t.Helper()
	require.NotNil(t, policy.TotalTimeout)
	got := time.Duration(*policy.TotalTimeout)
	require.GreaterOrEqual(t, got, min, "captured timeout %s was below minimum %s", got, min)
	require.LessOrEqual(t, got, max, "captured timeout %s was above maximum %s", got, max)
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
	requireCapturedTimeoutBetween(t, stub.policies[0], 119*time.Second, 2*time.Minute)
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
	requireCapturedTimeoutBetween(t, stub.policies[0], 4*time.Second, 5*time.Second)
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
	requireCapturedTimeoutBetween(t, stub.policies[0], 1*time.Millisecond, 50*time.Millisecond)
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
	requireCapturedTimeoutBetween(t, stub.policies[0], 1*time.Millisecond, 20*time.Millisecond)
}

func TestSequenceTimeoutFailsWhenNestedTaskExceedsBudget(t *testing.T) {
	opType := "timeout-sequence-fail-op"
	withRegisteredOps(t, newTimeoutTestOp(t, opType, 0))
	jobCtx, gitCtx := GenerateTestContext()
	stub, wfCtx := newTimeoutWorkflow(t, gitCtx, 40*time.Millisecond)

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeSequence{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{
					ID:      "root-sequence",
					Timeout: recipe.Duration(10 * time.Millisecond),
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
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded), "expected deadline exceeded error, got %v", err)
	require.Equal(t, 1, stub.calls)
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
	requireCapturedTimeoutBetween(t, stub.policies[0], 1*time.Millisecond, 50*time.Millisecond)
}
