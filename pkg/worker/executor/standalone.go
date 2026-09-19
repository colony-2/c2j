package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	ops2 "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/worker/ops"
	workflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"go.uber.org/zap"
)

type StandaloneExecutor struct {
	allocation execution.Allocation
	registry   *ops.ActivityRegistry
	logger     *zap.Logger
	deps       ops2.ServiceDependencies2
}

// NewStandaloneExecutor creates a new standalone recipe executor
func NewStandaloneExecutor(deps ops2.ServiceDependencies2, registry *ops.ActivityRegistry, logger *zap.Logger, allocations ...execution.Allocation) (*StandaloneExecutor, error) {
	allocation := execution.Allocation{SchemaVersion: execution.SchemaVersion}
	if len(allocations) > 0 {
		allocation = allocations[0]
	}
	allocation, err := allocation.Normalize()
	if err != nil {
		return nil, err
	}
	return &StandaloneExecutor{
		allocation: allocation,
		registry:   registry,
		logger:     logger,
		deps:       deps,
	}, nil
}

// Execute runs a recipe with the given inputs
func (e *StandaloneExecutor) Execute(
	ctx context.Context,
	r recipe.Recipe,
	inputs map[string]interface{},
	jobCtx contextual.JobContext,
	gitRef string,
) (map[string]interface{}, error) {
	return e.ExecuteWithRegistry(ctx, r, inputs, jobCtx, gitRef, func(_ string, recipeRef string) (*recipe.Recipe, error) {
		if recipeRef != r.GetMetadata().ID {
			return nil, fmt.Errorf("unknown recipe %s", recipeRef)
		}
		return &r, nil
	})
}

// ExecuteWithRegistry runs a recipe with a custom registry for resolving recipe references.
func (e *StandaloneExecutor) ExecuteWithRegistry(
	ctx context.Context,
	r recipe.Recipe,
	inputs map[string]interface{},
	jobCtx contextual.JobContext,
	gitRef string,
	registry workflow.RecipeProjectProvider,
) (map[string]interface{}, error) {
	rootResolver := compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{
		RecipeRefResolver: compiler.NewProviderBackedRecipeRefResolver(func(projectID string, recipeRef string) (*recipe.Recipe, error) {
			return registry(projectID, recipeRef)
		}),
	})

	control := &workflow.SWFWorkflowControl{
		Registry:                      registry,
		PreferRuntimeRecipeResolution: true,
	}

	deps := ops2.NewServiceDepsBuilder().
		WithWorkflowControl(control).
		WithDatabase(e.deps.Database()).
		WithSSEManager(e.deps.SSEManager()).
		Build()

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	runtime := toyruntime.New()
	events := make(chan compiler.ExecutionHandoff, 1)
	onHandoff := func(event compiler.ExecutionHandoff) {
		select {
		case events <- event:
		default:
		}
		stop()
	}
	executionRuntime := executionruntime.New(runtime, e.allocation, onHandoff)
	workset, err := compiler.NewRecipeWorkerWithOptions(deps, e.registry, compiler.RecipeJobWorkerOptions{
		Allocation: e.allocation, StageExecution: executionRuntime.Stage, OnExecutionHandoff: onHandoff,
		RootSourceResolver: rootResolver,
	})
	if err != nil {
		return nil, err
	}

	taskWorkers := make([]jobworkflow.TaskWorker, 0, len(workset.TaskWorkers))
	for _, tw := range workset.TaskWorkers {
		taskWorkers = append(taskWorkers, tw)
	}
	tenantID := "default"
	eng, err := jobworkflow.NewEngineBuilder().
		WithRuntime(executionRuntime).
		WithWorkerTenantId(tenantID).
		PlusWorkers(workset.JobWorker, taskWorkers...).
		BuildEngine()
	if err != nil {
		return nil, err
	}
	eng = jobdbschema.WorkflowEngine{Engine: eng, Registry: runtime}
	go eng.Run(runCtx)
	control.Engine = eng

	job := workflowctl.StartJob{
		TenantId:   tenantID,
		RecipeName: r.GetMetadata().ID,
		Inputs:     inputs,
		JobContext: jobCtx,
		GitRef:     gitRef,
	}

	jobKey, err := control.StartJob(ctx, job)
	if err != nil {
		return nil, err
	}
	if err := jobworkflow.WaitForJobToComplete(runCtx, 30*time.Second, jobKey, eng); err != nil {
		select {
		case event := <-events:
			return nil, &EnvironmentRequiredError{Handoff: event}
		default:
		}
		return nil, err
	}
	out, err := swfutil.JobResult(ctx, eng, jobKey)
	if err != nil {
		return nil, err
	}
	d, err := out.GetData()
	if err != nil {
		return nil, err
	}
	outMap := make(map[string]interface{})
	err = json.Unmarshal(d, &outMap)
	return outMap, err
}

// EnvironmentRequiredError reports that a disposable standalone runtime cannot
// migrate itself. Durable production execution should use a persistent runtime.
type EnvironmentRequiredError struct{ Handoff compiler.ExecutionHandoff }

func (e *EnvironmentRequiredError) Error() string {
	return "execution environment required; standalone executor cannot provision a replacement"
}

// GetActivityRegistry returns the activity registry
func (e *StandaloneExecutor) GetActivityRegistry() *ops.ActivityRegistry {
	return e.registry
}
