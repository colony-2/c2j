package compiler

import (
	"context"
	"log/slog"
	"testing"

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

// Regression guard: a single-op recipe should surface the op's outputs (lastExecution) back to the caller.
func TestExecuteRecipeSingleOpReturnsOutputs(t *testing.T) {
	originalOps := coreops.List()
	t.Cleanup(func() {
		coreops.Clear()
		if len(originalOps) > 0 {
			coreops.Register(originalOps...)
		}
	})
	coreops.Clear()

	type output struct {
		Value bool `json:"value"`
	}

	opType := "single-op-output"
	op, err := coreops.NewOp().
		WithType(opType).
		AddStep(opType, coreops.NewStepWithDeps(func(_ coreops.OpDependencies, _ context.Context, in map[string]interface{}) (output, error) {
			return output{Value: true}, nil
		})).
		Build()
	require.NoError(t, err)
	coreops.Register(op.(coreops.RegisterableOp))

	jobCtx, gitCtx := GenerateTestContext()

	envelope := workerops.ActivityInvocationOutput{
		OpOutput: map[string]interface{}{"value": true},
		GitResult: contextual.GitCommitContext{
			PersistHash: gitCtx.PersistHash,
			ParentHash:  gitCtx.ParentHash,
			ParentRef:   gitCtx.ParentRef,
		},
		NextTask: "",
	}
	outEnv, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, envelope)
	require.NoError(t, err)
	taskData := jobdb.NewTaskDataOrPanic(outEnv)

	stub := &stubJobContext{
		out:      taskData,
		jobKey:   jobdb.JobKey{TenantId: "test-tenant", JobId: "stub-job"},
		taskType: opType + ":" + opType,
	}

	ctx := workflow.Context{
		JobContext:           stub,
		ServiceDependencies2: coreops.NewServiceDepsBuilder().Build(),
	}

	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeOp{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{
					ID:     "single-op-recipe",
					Inputs: map[string]interface{}{},
				},
			},
			OpData: recipe.OpData{Op: opType},
		},
	}

	result, _, err := ExecuteRecipe(ctx, rec, map[string]interface{}{}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{"value": true}, result, "ExecuteRecipe should return op outputs for single-op recipes")
	require.Equal(t, 1, stub.calls)
	require.Equal(t, stub.taskType, stub.lastTaskType)
}

// Minimal JobContext stub to capture DoTask invocations.
type stubJobContext struct {
	noLeaseClientContext
	jobKey       jobdb.JobKey
	out          jobdb.TaskData
	calls        int
	taskType     string
	lastTaskType string
}

func (s *stubJobContext) AwaitJobs(jobIds ...string) error {
	return nil
}

var _ jobworkflow.ExecutionContext = &stubJobContext{}

func (s *stubJobContext) GetJobKey() jobdb.JobKey            { return s.jobKey }
func (s *stubJobContext) Logger() *slog.Logger               { return slog.Default() }
func (s *stubJobContext) AwaitDuration(jobdb.Duration) error { return nil }

func (s *stubJobContext) DoTask(call testTaskCall, build func(testContext) (jobdb.TaskData, error)) (jobdb.TaskData, error) {
	data, buildErr := build(s.Context())
	if buildErr != nil {
		return nil, buildErr
	}
	_ = data
	taskType := call.TaskType
	_ = taskType

	s.calls++
	s.lastTaskType = taskType
	return s.out, nil
}

func (c *stubJobContext) DoScope(site string, opts testScopeOptions, body func(testExecutionContext) (jobdb.JobData, error)) (jobdb.JobData, error) {
	return runMockScope(c, site, opts, body)
}
func (c *stubJobContext) Context() testContext { return testBackground() }
func (c *stubJobContext) ExecutionVersions() jobdb.ExecutionVersions {
	return jobdb.ExecutionVersions{}
}
