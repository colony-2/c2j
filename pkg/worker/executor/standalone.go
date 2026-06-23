package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
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
	registry *ops.ActivityRegistry
	logger   *zap.Logger
	deps     ops2.ServiceDependencies2
}

// NewStandaloneExecutor creates a new standalone recipe executor
func NewStandaloneExecutor(deps ops2.ServiceDependencies2, registry *ops.ActivityRegistry, logger *zap.Logger) (*StandaloneExecutor, error) {
	return &StandaloneExecutor{
		registry: registry,
		logger:   logger,
		deps:     deps,
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

	workset, err := compiler.NewRecipeWorkerWithOptions(deps, e.registry, compiler.RecipeJobWorkerOptions{
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
		WithRuntime(toyruntime.New()).
		WithWorkerTenantId(tenantID).
		PlusWorkers(workset.JobWorker, taskWorkers...).
		BuildEngine()
	if err != nil {
		return nil, err
	}
	go eng.Run(ctx)
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
	if err := jobworkflow.WaitForJobToComplete(ctx, 30*time.Second, jobKey, eng); err != nil {
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

// GetActivityRegistry returns the activity registry
func (e *StandaloneExecutor) GetActivityRegistry() *ops.ActivityRegistry {
	return e.registry
}
