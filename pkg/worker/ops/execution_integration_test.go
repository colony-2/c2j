package ops_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/worker/executor"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestExecutionDirectiveFromRealOperationReachesStandaloneHandoff(t *testing.T) {
	var called atomic.Int32
	memory := "16Gi"
	op, err := coreops.NewOp().WithType("execution_boundary_integration").AddStep("work", coreops.NewStepWithDeps(func(deps coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
		called.Add(1)
		if err := coreops.SuspendExecution(deps, execution.Requirements{Resources: execution.Resources{Memory: &memory}}); err != nil {
			return nil, err
		}
		return map[string]any{"manifest": "recorded"}, nil
	})).Build()
	require.NoError(t, err)
	coreops.Register(op.(coreops.RegisterableOp))
	r, err := recipe.LoadRecipeFromString([]byte("id: execution_boundary\nexecution:\n  resources:\n    memory: 2Gi\nop: execution_boundary_integration\n"))
	require.NoError(t, err)
	repo, base, cleanup := createTempRepo(t)
	defer cleanup()
	jobCtx := contextual.JobContext{
		Workflow: contextual.WorkflowContext{CellName: "cells/test-cell"},
		GitBase:  contextual.GitBaseContext{BaseRepo: repo, BaseRef: base, ResolvedBaseHash: base, GitAuthor: "Test User <test@example.com>"},
	}
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	small := "2Gi"
	runner, err := executor.NewStandaloneExecutor(coreops.NewServiceDepsBuilder().Build(), registry, zaptest.NewLogger(t), execution.Allocation{SchemaVersion: 1, Resources: execution.Resources{Memory: &small}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = runner.Execute(ctx, *r, nil, jobCtx, base)
	var handoff *executor.EnvironmentRequiredError
	require.ErrorAs(t, err, &handoff)
	require.NoError(t, ctx.Err())
	require.EqualValues(t, 1, called.Load())
	require.Equal(t, "16Gi", *handoff.Handoff.Demand.Effective.Resources.Memory)
	require.True(t, handoff.Handoff.Published)
}
