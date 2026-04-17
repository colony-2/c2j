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
	"github.com/colony-2/swf-go/pkg/swf"
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
	taskData := swf.NewTaskDataOrPanic(outEnv)

	stub := &stubJobContext{
		out:      taskData,
		jobKey:   swf.JobKey{TenantId: "test-tenant", JobId: "stub-job"},
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
	jobKey       swf.JobKey
	out          swf.TaskData
	calls        int
	taskType     string
	lastTaskType string
}

func (s *stubJobContext) AwaitJobs(jobIds ...string) error {
	return nil
}

var _ swf.JobContext = &stubJobContext{}

func (s *stubJobContext) GetJobKey() swf.JobKey            { return s.jobKey }
func (s *stubJobContext) Logger() *slog.Logger             { return slog.Default() }
func (s *stubJobContext) AwaitDuration(swf.Duration) error { return nil }

func (s *stubJobContext) DoTask(_ swf.RunPolicy, taskType string, data swf.TaskData) (swf.TaskData, error) {
	s.calls++
	s.lastTaskType = taskType
	return s.out, nil
}
