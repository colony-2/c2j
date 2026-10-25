package ops_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestExecutionDirectiveFromRealOperationReachesSuppliedLeaseOutcome(t *testing.T) {
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
	r, err := recipe.LoadRecipeFromString([]byte("id: execution_boundary\nexecution:\n  resources:\n    memory: 2Gi\nsequence:\n  - id: discover\n    op: execution_boundary_integration\n  - id: next\n    op: execution_boundary_integration\n"))
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
	rt := toy.New()
	wrapped := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1, Resources: execution.Resources{Memory: &small}})
	ws, err := compiler.NewRecipeWorkerWithOptions(coreops.NewServiceDepsBuilder().Build(), registry, compiler.RecipeJobWorkerOptions{Allocation: wrapped.Allocation, StageExecution: wrapped.Stage, WrapTaskWorker: wrapped.WrapTaskWorker})
	require.NoError(t, err)
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	keyRef, err := starter.StartRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "test", RecipeName: r.GetMetadata().ID, JobContext: jobCtx, GitRef: base}, jobdbschema.WorkflowEngine{Engine: engine, Registry: rt}, *r)
	key := keyRef.Key()
	require.NoError(t, err)
	lease, err := rt.GetJobLease(context.Background(), jobdb.GetJobLeaseRequest{JobKey: key, Routes: []jobdb.Route{{JobType: "recipe"}}, WorkerID: "test", LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.NotNil(t, lease)
	var tasks []jobworkflow.TaskWorker
	for _, w := range ws.TaskWorkers {
		tasks = append(tasks, w)
	}
	outcome, err := wrapped.RunWithLease(context.Background(), lease, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: tasks}, nil)
	require.NoError(t, err)
	require.Equal(t, jobworkflow.JobRunSuspended, outcome.Status)
	handoff, err := compiler.ExecutionHandoffFromOutcome(outcome)
	require.NoError(t, err)
	require.NotNil(t, handoff)
	require.EqualValues(t, 1, called.Load())
	require.Equal(t, "16Gi", *handoff.Demand.Effective.Resources.Memory)
	require.True(t, handoff.Published)
}
