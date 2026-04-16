package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/colony-2/c2j/core/pkg/logutil"
	"github.com/colony-2/c2j/recipe-core/pkg/contextual"
	"github.com/colony-2/c2j/recipe-core/pkg/ops"
	"github.com/colony-2/c2j/recipe-core/pkg/recipe"
	"github.com/colony-2/c2j/recipe-core/pkg/starter"
	"github.com/colony-2/c2j/recipe-core/pkg/workflow"
	"github.com/colony-2/c2j/recipe-core/pkg/workflowctl"
	"github.com/colony-2/c2j/recipe-template/pkg/template"
	workerops "github.com/colony-2/c2j/recipe-worker/pkg/ops"
	"github.com/colony-2/swf-go/pkg/swf"
)

type RecipeJobWorkerOptions struct {
	CELOptionsProvider template.CELOptionsProvider

	// Executor overrides recipe execution for instrumentation. When nil, DefaultRecipeExecutor is used.
	Executor RecipeExecutor
	// ExecutorFactory overrides Executor when provided, allowing per-run executors.
	ExecutorFactory func() RecipeExecutor

	// RootSourceResolver resolves non-embedded root recipe selectors at execution time.
	RootSourceResolver RecipeSourceResolver

	// OnRecipeLoaded is called after the recipe artifact has been loaded and parsed.
	OnRecipeLoaded func(recipeName string)
	// OnRecipeSourceResolved is called when a non-embedded root recipe selector is resolved.
	OnRecipeSourceResolved func(RecipeSourceResolution)
}

type recipeJobWorker struct {
	celProvider      template.CELOptionsProvider
	executor         RecipeExecutor
	executorFactory  func() RecipeExecutor
	rootResolver     RecipeSourceResolver
	onRecipeLoadedFn func(recipeName string)
	onSourceResolved func(RecipeSourceResolution)
}

func NewRecipeJobWorker(opts RecipeJobWorkerOptions) swf.JobWorker {
	return &recipeJobWorker{
		celProvider:      opts.CELOptionsProvider,
		executor:         opts.Executor,
		executorFactory:  opts.ExecutorFactory,
		rootResolver:     opts.RootSourceResolver,
		onRecipeLoadedFn: opts.OnRecipeLoaded,
		onSourceResolved: opts.OnRecipeSourceResolved,
	}
}

func NewRecipeWorker(dependencies ops.ServiceDependencies2, activityRegistry *workerops.ActivityRegistry, provider ...template.CELOptionsProvider) (*swf.WorkSet, error) {
	opts := RecipeJobWorkerOptions{}
	if len(provider) > 0 {
		opts.CELOptionsProvider = provider[0]
	}
	return NewRecipeWorkerWithOptions(dependencies, activityRegistry, opts)
}

func NewRecipeWorkerWithOptions(dependencies ops.ServiceDependencies2, activityRegistry *workerops.ActivityRegistry, opts RecipeJobWorkerOptions) (*swf.WorkSet, error) {
	job := NewRecipeJobWorker(opts)
	taskWorkers := activityRegistry.GetTaskWorkers(dependencies)
	if resolutionWorker := newRootSourceResolutionTaskWorker(opts.RootSourceResolver); resolutionWorker != nil {
		taskWorkers = append(taskWorkers, resolutionWorker)
	}
	return swf.AsWorkSet(job, taskWorkers...)
}

func (j recipeJobWorker) Name() string {
	return starter.RecipeJobType
}

func (j recipeJobWorker) Run(ctx swf.JobContext, jobData swf.JobData) (swf.JobData, error) {
	jobKey := ctx.GetJobKey()
	logger := ctx.Logger()
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(
		"tenant_id", jobKey.TenantId,
		"job_id", jobKey.JobId,
		"job_type", starter.RecipeJobType,
	)
	artifacts, err := jobData.GetArtifacts()
	if err != nil {
		logger.Error("recipe job: failed to load artifacts",
			"error", err,
			"error_chain", logutil.ErrorChain(err),
			"stacktrace", logutil.Stacktrace(5),
		)
		return nil, err
	}
	recipes := newRetriever(artifacts)

	data, err := jobData.GetData()
	if err != nil {
		logger.Error("recipe job: failed to read job data",
			"error", err,
			"error_chain", logutil.ErrorChain(err),
			"stacktrace", logutil.Stacktrace(5),
		)
		return nil, err
	}
	input := workflowctl.StartJob{}
	err = json.Unmarshal(data, &input)
	if err != nil {
		logger.Error("recipe job: failed to unmarshal start job payload",
			"error", err,
			"error_chain", logutil.ErrorChain(err),
			"stacktrace", logutil.Stacktrace(5),
		)
		return nil, err
	}

	if input.RecipeName == "" {
		err := fmt.Errorf("missing recipe name")
		logger.Warn("recipe job: invalid start payload", "error", err)
		return nil, err
	}
	logger = logger.With("recipe_name", input.RecipeName)

	if j.onRecipeLoadedFn != nil {
		j.onRecipeLoadedFn(input.RecipeName)
	}

	resolution := RecipeSourceResolution{
		SourceKind:        RecipeSourceKindArtifact,
		SubmittedSelector: input.RecipeName,
		ResolvedSelector:  input.RecipeName,
		ArtifactName:      input.RecipeName + starter.RecipeArtifactSuffix,
		WasAlreadyPinned:  true,
	}
	resolvedSource := ResolvedRecipeSource{RecipeSourceResolution: resolution}

	if !recipes.HasRecipe(input.RecipeName) {
		taskInput, err := swf.NewTaskData(rootSourceResolutionTaskInput{
			ProjectID: strings.TrimSpace(input.TenantId),
			Selector:  input.RecipeName,
		})
		if err != nil {
			logger.Error("recipe job: failed to encode root recipe source resolution input",
				"error", err,
				"error_chain", logutil.ErrorChain(err),
				"stacktrace", logutil.Stacktrace(5),
			)
			return nil, err
		}

		taskOutput, err := ctx.DoTask(swf.RunPolicy{}, RootSourceResolutionTaskType, taskInput)
		if err != nil {
			logger.Error("recipe job: root recipe source resolution task failed",
				"error", err,
				"error_chain", logutil.ErrorChain(err),
				"stacktrace", logutil.Stacktrace(5),
			)
			return nil, err
		}

		parsedSource, err := ParseResolvedRecipeSourceTaskData(taskOutput)
		if err != nil {
			logger.Error("recipe job: failed to decode root recipe source resolution output",
				"error", err,
				"error_chain", logutil.ErrorChain(err),
				"stacktrace", logutil.Stacktrace(5),
			)
			return nil, err
		}
		resolvedSource = *parsedSource
		resolution = resolvedSource.RecipeSourceResolution

		if j.onSourceResolved != nil {
			j.onSourceResolved(resolution)
		}
		logger = logger.With(
			"recipe_source_kind", resolution.SourceKind,
			"recipe_source_selector", resolution.EffectiveSelector(),
			"recipe_source_commit", resolution.ResolvedCommit,
		)
	}

	var r recipe.Recipe
	if resolution.SourceKind == RecipeSourceKindArtifact {
		r, err = recipes.GetRecipe(input.RecipeName)
	} else if strings.TrimSpace(resolvedSource.RecipeYAML) != "" {
		r, err = resolvedSource.LoadRecipe()
	} else {
		if j.rootResolver == nil {
			err = fmt.Errorf("recipe source resolver not configured to load non-artifact selector %q", resolution.EffectiveSelector())
			logger.Error("recipe job: failed to load recipe",
				"error", err,
				"error_chain", logutil.ErrorChain(err),
				"stacktrace", logutil.Stacktrace(5),
			)
			return nil, err
		}
		r, err = j.rootResolver.Load(context.Background(), strings.TrimSpace(input.TenantId), resolution)
	}
	if err != nil {
		logger.Error("recipe job: failed to load recipe",
			"error", err,
			"error_chain", logutil.ErrorChain(err),
			"stacktrace", logutil.Stacktrace(5),
		)
		return nil, err
	}

	runContext := input.JobContext
	applyRootRecipeSource(&runContext, resolution)
	err = ensureSentinel(&runContext.Environment.WorktreePath, contextual.WorktreePathSentinel, "worktree path")
	if err != nil {
		logger.Error("recipe job: invalid worktree sentinel",
			"error", err,
			"error_chain", logutil.ErrorChain(err),
			"stacktrace", logutil.Stacktrace(5),
		)
		return nil, err
	}
	err = ensureSentinel(&runContext.Environment.WorkdirPath, contextual.WorkdirPathSentinel, "workdir path")
	if err != nil {
		logger.Error("recipe job: invalid workdir sentinel",
			"error", err,
			"error_chain", logutil.ErrorChain(err),
			"stacktrace", logutil.Stacktrace(5),
		)
		return nil, err
	}
	err = ensureSentinel(&runContext.Environment.ArtifactInbox, contextual.ArtifactInboxSentinel, "artifact inbox")
	if err != nil {
		logger.Error("recipe job: invalid artifact inbox sentinel",
			"error", err,
			"error_chain", logutil.ErrorChain(err),
			"stacktrace", logutil.Stacktrace(5),
		)
		return nil, err
	}
	err = ensureSentinel(&runContext.Environment.ArtifactOutbox, contextual.ArtifactOutboxSentinel, "artifact outbox")
	if err != nil {
		logger.Error("recipe job: invalid artifact outbox sentinel",
			"error", err,
			"error_chain", logutil.ErrorChain(err),
			"stacktrace", logutil.Stacktrace(5),
		)
		return nil, err
	}
	err = ensureSentinel(&runContext.Workflow.JobID, contextual.JobIdSentinel, "job id")
	if err != nil {
		logger.Error("recipe job: invalid job id sentinel",
			"error", err,
			"error_chain", logutil.ErrorChain(err),
			"stacktrace", logutil.Stacktrace(5),
		)
		return nil, err
	}

	wCtx := workflow.Context{JobContext: ctx}
	opts := ExecutionOptions{}
	if j.celProvider != nil {
		opts.CELOptionsProvider = j.celProvider
	}

	exec := j.executor
	if j.executorFactory != nil {
		exec = j.executorFactory()
	}
	if exec == nil {
		exec = DefaultRecipeExecutor{}
	}
	out, artifacts, err := ExecuteRecipeWithExecutor(exec, wCtx, r, input.Inputs, runContext, contextual.GitCommitContext{ParentRef: input.GitRef}, opts)

	if err != nil {
		return nil, err
	}
	taskData, err := swf.NewTaskData(out, artifacts...)
	if err != nil {
		logger.Error("recipe job: failed to create task data",
			"error", err,
			"error_chain", logutil.ErrorChain(err),
			"stacktrace", logutil.Stacktrace(5),
		)
		return nil, err
	}
	logger.Info("recipe execution completed successfully")
	return swf.JobData(taskData), nil

}

func ensureSentinel(field *string, sentinel string, name string) error {
	if *field == sentinel {
		return nil
	}

	if *field != "" {
		return fmt.Errorf("unexpected %s: %s", name, *field)
	}

	*field = sentinel
	return nil
}

var _ swf.JobWorker = &recipeJobWorker{}

func applyRootRecipeSource(runContext *contextual.JobContext, resolution RecipeSourceResolution) {
	if runContext == nil {
		return
	}
	if resolution.SourceKind != RecipeSourceKindGit {
		runContext.RecipeSource = contextual.RecipeSourceContext{}
		return
	}

	parsed, err := parseGitRecipeSelector(resolution.EffectiveSelector())
	if err != nil {
		return
	}

	runContext.RecipeSource = contextual.RecipeSourceContext{
		Repo:     parsed.RepositoryURL,
		Ref:      parsed.Ref,
		Path:     parsed.RecipePath,
		Selector: resolution.EffectiveSelector(),
	}
}
