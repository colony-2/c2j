package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/logutil"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/template"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
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
	// OnRecipeSourceResolved is called after the root recipe source has been resolved.
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

func NewRecipeJobWorker(opts RecipeJobWorkerOptions) jobworkflow.JobWorker {
	return &recipeJobWorker{
		celProvider:      opts.CELOptionsProvider,
		executor:         opts.Executor,
		executorFactory:  opts.ExecutorFactory,
		rootResolver:     opts.RootSourceResolver,
		onRecipeLoadedFn: opts.OnRecipeLoaded,
		onSourceResolved: opts.OnRecipeSourceResolved,
	}
}

func NewRecipeWorker(dependencies ops.ServiceDependencies2, activityRegistry *workerops.ActivityRegistry, provider ...template.CELOptionsProvider) (*jobworkflow.WorkSet, error) {
	opts := RecipeJobWorkerOptions{}
	if len(provider) > 0 {
		opts.CELOptionsProvider = provider[0]
	}
	return NewRecipeWorkerWithOptions(dependencies, activityRegistry, opts)
}

func NewRecipeWorkerWithOptions(dependencies ops.ServiceDependencies2, activityRegistry *workerops.ActivityRegistry, opts RecipeJobWorkerOptions) (*jobworkflow.WorkSet, error) {
	job := NewRecipeJobWorker(opts)
	taskWorkers := activityRegistry.GetTaskWorkers(dependencies)
	if resolutionWorker := newRootSourceResolutionTaskWorker(opts.RootSourceResolver); resolutionWorker != nil {
		taskWorkers = append(taskWorkers, resolutionWorker)
	}
	taskWorkers = append(taskWorkers, newWithinRecipeResolutionTaskWorker())
	return jobworkflow.AsWorkSet(job, taskWorkers...)
}

func (j recipeJobWorker) Name() string {
	return starter.RecipeJobType
}

func (j recipeJobWorker) Run(ctx jobworkflow.JobContext, jobData jobdb.JobData) (jobdb.JobData, error) {
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

	hasEmbeddedRecipeArtifact := recipes.HasRecipe(input.RecipeName)
	resolution := RecipeSourceResolution{
		SourceKind:        RecipeSourceKindArtifact,
		SubmittedSelector: input.RecipeName,
		ResolvedSelector:  input.RecipeName,
		ArtifactName:      input.RecipeName + starter.RecipeArtifactSuffix,
		WasAlreadyPinned:  true,
	}
	var r recipe.Recipe
	if hasEmbeddedRecipeArtifact {
		r, err = recipes.GetRecipe(input.RecipeName)
	} else {
		if j.rootResolver == nil {
			err = fmt.Errorf("recipe source resolver not configured to load non-artifact selector %q", input.RecipeName)
			logger.Error("recipe job: failed to load recipe",
				"error", err,
				"error_chain", logutil.ErrorChain(err),
				"stacktrace", logutil.Stacktrace(5),
			)
			return nil, err
		}
		taskInput, err := jobdb.NewTaskData(rootSourceResolutionTaskInput{
			ProjectID:  strings.TrimSpace(input.TenantId),
			Selector:   input.RecipeName,
			LookupRepo: rootRecipeLookupRepo(input.JobContext),
			LookupRef:  rootRecipeLookupRef(input.JobContext),
		})
		if err != nil {
			logger.Error("recipe job: failed to encode root recipe source resolution input",
				"error", err,
				"error_chain", logutil.ErrorChain(err),
				"stacktrace", logutil.Stacktrace(5),
			)
			return nil, err
		}

		taskOutput, err := ctx.DoTask(jobdb.RunPolicy{}, RootSourceResolutionTaskType, taskInput)
		if err != nil {
			if logReplayCacheMiss(logger, "recipe job: root recipe source resolution replay cache miss", err) {
				return nil, err
			}
			logger.Error("recipe job: root recipe source resolution task failed",
				"error", err,
				"error_chain", logutil.ErrorChain(err),
				"stacktrace", logutil.Stacktrace(5),
			)
			return nil, err
		}

		resolvedSource, err := ParseResolvedRecipeSourceTaskData(taskOutput)
		if err != nil {
			logger.Error("recipe job: failed to decode root recipe source resolution output",
				"error", err,
				"error_chain", logutil.ErrorChain(err),
				"stacktrace", logutil.Stacktrace(5),
			)
			return nil, err
		}
		resolution = resolvedSource.RecipeSourceResolution

		if strings.TrimSpace(resolvedSource.RecipeYAML) != "" {
			r, err = resolvedSource.LoadRecipe()
		} else {
			r, err = j.rootResolver.Load(context.Background(), strings.TrimSpace(input.TenantId), resolution)
		}
	}
	if err != nil {
		logger.Error("recipe job: failed to load recipe",
			"error", err,
			"error_chain", logutil.ErrorChain(err),
			"stacktrace", logutil.Stacktrace(5),
		)
		return nil, err
	}

	if j.onSourceResolved != nil {
		j.onSourceResolved(resolution)
	}
	logger = logger.With(
		"recipe_source_kind", resolution.SourceKind,
		"recipe_source_selector", resolution.EffectiveSelector(),
		"recipe_source_commit", resolution.ResolvedCommit,
	)

	runContext := input.JobContext
	submittedRefs, err := submittedArtifactRefs(input, artifacts, hasEmbeddedRecipeArtifact)
	if err != nil {
		logger.Error("recipe job: failed to index submitted artifacts",
			"error", err,
			"error_chain", logutil.ErrorChain(err),
			"stacktrace", logutil.Stacktrace(5),
		)
		return nil, err
	}
	runContext.Artifacts = submittedRefs
	applyRootRecipeSource(&runContext, resolution)
	if err := ensureEnvironmentSentinels(&runContext.Environment); err != nil {
		logger.Error("recipe job: invalid environment sentinel",
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
	opts.ResolvedGitRefs = gitRefPinsFromRecipeSource(resolution)

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
	taskData, err := jobdb.NewTaskData(out, artifacts...)
	if err != nil {
		logger.Error("recipe job: failed to create task data",
			"error", err,
			"error_chain", logutil.ErrorChain(err),
			"stacktrace", logutil.Stacktrace(5),
		)
		return nil, err
	}
	logger.Info("recipe execution completed successfully")
	return jobdb.JobData(taskData), nil

}

func submittedArtifactRefs(input workflowctl.StartJob, jobArtifacts []jobdb.Artifact, hasEmbeddedRecipeArtifact bool) (map[string]recipeartifacts.Ref, error) {
	out := make(map[string]recipeartifacts.Ref)
	for _, artifactRef := range input.ArtifactRefs {
		if artifactRef.IsZero() {
			continue
		}
		if err := artifactRef.Validate(); err != nil {
			return nil, fmt.Errorf("invalid submitted artifact ref %q: %w", artifactRef.NameValue(), err)
		}
		if err := addSubmittedArtifactRef(out, artifactRef); err != nil {
			return nil, err
		}
	}

	internalRecipeArtifactName := input.RecipeName + starter.RecipeArtifactSuffix
	for _, artifact := range jobArtifacts {
		if artifact == nil {
			continue
		}
		if hasEmbeddedRecipeArtifact && artifact.Name() == internalRecipeArtifactName {
			continue
		}
		artifactRef, err := recipeartifacts.RefFromArtifact(artifact)
		if err != nil {
			return nil, fmt.Errorf("build submitted artifact ref for %q: %w", artifact.Name(), err)
		}
		if err := addSubmittedArtifactRef(out, artifactRef); err != nil {
			return nil, err
		}
	}

	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func addSubmittedArtifactRef(refs map[string]recipeartifacts.Ref, artifactRef recipeartifacts.Ref) error {
	name := artifactRef.NameValue()
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("submitted artifact ref name cannot be empty")
	}
	if existing, exists := refs[name]; exists {
		if existing.Identity() != artifactRef.Identity() {
			return fmt.Errorf("duplicate submitted artifact name %q refers to both %s and %s", name, existing.Identity(), artifactRef.Identity())
		}
		return nil
	}
	refs[name] = artifactRef
	return nil
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

func ensureEnvironmentSentinels(env *contextual.EnvironmentContext) error {
	if err := ensureSentinel(&env.WorktreePath, contextual.WorktreePathSentinel, "worktree path"); err != nil {
		return err
	}
	if err := ensureSentinel(&env.WorkdirPath, contextual.WorkdirPathSentinel, "workdir path"); err != nil {
		return err
	}
	if err := ensureSentinel(&env.ArtifactInbox, contextual.ArtifactInboxSentinel, "artifact inbox"); err != nil {
		return err
	}
	if err := ensureSentinel(&env.ArtifactOutbox, contextual.ArtifactOutboxSentinel, "artifact outbox"); err != nil {
		return err
	}

	if err := ensureSentinel(&env.Host.WorktreePath, contextual.WorktreePathSentinel, "host worktree path"); err != nil {
		return err
	}
	if err := ensureSentinel(&env.Host.Workdir, contextual.WorkdirPathSentinel, "host workdir path"); err != nil {
		return err
	}
	if err := ensureSentinel(&env.Host.Inbox, contextual.ArtifactInboxSentinel, "host artifact inbox"); err != nil {
		return err
	}
	if err := ensureSentinel(&env.Host.Outbox, contextual.ArtifactOutboxSentinel, "host artifact outbox"); err != nil {
		return err
	}

	if err := ensureSentinel(&env.Op.WorktreePath, contextual.OpWorktreePathSentinel, "op worktree path"); err != nil {
		return err
	}
	if err := ensureSentinel(&env.Op.Workdir, contextual.OpWorkdirPathSentinel, "op workdir path"); err != nil {
		return err
	}
	if err := ensureSentinel(&env.Op.Inbox, contextual.OpArtifactInboxSentinel, "op artifact inbox"); err != nil {
		return err
	}
	if err := ensureSentinel(&env.Op.Outbox, contextual.OpArtifactOutboxSentinel, "op artifact outbox"); err != nil {
		return err
	}
	return nil
}

var _ jobworkflow.JobWorker = &recipeJobWorker{}

func rootRecipeLookupRepo(jobContext contextual.JobContext) string {
	return strings.TrimSpace(jobContext.RecipeSource.Repo)
}

func rootRecipeLookupRef(jobContext contextual.JobContext) string {
	return strings.TrimSpace(jobContext.RecipeSource.Ref)
}

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
