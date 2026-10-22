package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/template"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type RecipeJobWorkerOptions struct {
	// Logger optionally overrides the runtime logger (for request-scoped replay).
	Logger           *slog.Logger
	ConsumerVersions map[string]string
	// SecretSessions is shared with the activity registry when registering workers individually.
	SecretSessions     *secrets.Sessions
	Allocation         execution.Allocation
	ReadOnlyReplay     bool
	StageExecution     func(jobdb.JobKey, execution.Demand)
	WrapTaskWorker     func(jobworkflow.TaskWorker) jobworkflow.TaskWorker
	CELOptionsProvider template.CELOptionsProvider
	BeforeScope        func(context.Context, string, *template.ResolutionContext) error

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
	logger              *slog.Logger
	secretSessions      *secrets.Sessions
	taskGuardConfigured bool
	allocation          execution.Allocation
	readOnlyReplay      bool
	stageExecution      func(jobdb.JobKey, execution.Demand)
	celProvider         template.CELOptionsProvider
	beforeScope         func(context.Context, string, *template.ResolutionContext) error
	executor            RecipeExecutor
	executorFactory     func() RecipeExecutor
	rootResolver        RecipeSourceResolver
	onRecipeLoadedFn    func(recipeName string)
	onSourceResolved    func(RecipeSourceResolution)
}

func NewRecipeJobWorker(opts RecipeJobWorkerOptions) jobworkflow.ExecutionWorker {
	j := &recipeJobWorker{
		logger:              opts.Logger,
		secretSessions:      opts.SecretSessions,
		taskGuardConfigured: opts.WrapTaskWorker != nil,
		allocation:          opts.Allocation, readOnlyReplay: opts.ReadOnlyReplay,
		stageExecution:   opts.StageExecution,
		celProvider:      opts.CELOptionsProvider,
		beforeScope:      opts.BeforeScope,
		executor:         opts.Executor,
		executorFactory:  opts.ExecutorFactory,
		rootResolver:     opts.RootSourceResolver,
		onRecipeLoadedFn: opts.OnRecipeLoaded,
		onSourceResolved: opts.OnRecipeSourceResolved,
	}
	return jobworkflow.ExecutionWorker{JobType: starter.RecipeJobType, Execute: j.Run}
}

func NewRecipeWorker(dependencies ops.ServiceDependencies2, activityRegistry *workerops.ActivityRegistry, provider ...template.CELOptionsProvider) (*jobworkflow.WorkSet, error) {
	opts := RecipeJobWorkerOptions{}
	if len(provider) > 0 {
		opts.CELOptionsProvider = provider[0]
	}
	return NewRecipeWorkerWithOptions(dependencies, activityRegistry, opts)
}

func NewRecipeWorkerWithOptions(dependencies ops.ServiceDependencies2, activityRegistry *workerops.ActivityRegistry, opts RecipeJobWorkerOptions) (*jobworkflow.WorkSet, error) {
	opts.SecretSessions = activityRegistry.SecretSessions
	job := NewRecipeJobWorker(opts)
	taskWorkers := activityRegistry.GetTaskWorkers(dependencies)
	if resolutionWorker := newRootSourceResolutionTaskWorker(opts.RootSourceResolver); resolutionWorker != nil {
		taskWorkers = append(taskWorkers, resolutionWorker)
	}
	taskWorkers = append(taskWorkers, newWithinRecipeResolutionTaskWorker(), NewWorkspaceResolutionTaskWorker())
	if opts.WrapTaskWorker != nil {
		for i, worker := range taskWorkers {
			taskWorkers[i] = opts.WrapTaskWorker(worker)
		}
	}
	return jobworkflow.AsWorkSetWithOptions(job, jobworkflow.WorkRegistrationOptions{ConsumerVersions: ExecutionConsumerVersions(opts.ConsumerVersions)}, taskWorkers...)
}

func (j recipeJobWorker) Name() string {
	return starter.RecipeJobType
}

func (j recipeJobWorker) Run(ctx jobworkflow.ExecutionContext, jobData jobdb.JobData) (jobdb.JobData, error) {
	jobKey := ctx.GetJobKey()
	if j.secretSessions != nil && !j.readOnlyReplay {
		redactor, err := j.secretSessions.OpenExecution(ctx.Context(), jobKey)
		if err != nil {
			return nil, err
		}
		defer j.secretSessions.CloseExecution(jobKey, redactor)
	}
	logger := j.logger
	if logger == nil {
		logger = ctx.Logger()
	}
	logger = secrets.MaskLogger(logger)
	if !j.readOnlyReplay {
		logger = logger.With("purpose", "run")
	}
	logger = logger.With(
		"component", "compiler",
		"tenant_id", jobKey.TenantId,
		"job_id", jobKey.JobId,
		"job_type", starter.RecipeJobType,
	)
	artifacts, err := jobData.GetArtifacts()
	if err != nil {
		logger.Debug("recipe job: failed to load artifacts",
			"error", err,
		)
		return nil, err
	}
	recipes := newRetriever(artifacts)

	data, err := jobData.GetData()
	if err != nil {
		logger.Debug("recipe job: failed to read job data",
			"error", err,
		)
		return nil, err
	}
	input := workflowctl.StartJob{}
	err = json.Unmarshal(data, &input)
	if err != nil {
		logger.Debug("recipe job: failed to unmarshal start job payload",
			"error", err,
		)
		return nil, err
	}

	if input.RecipeName == "" {
		err := fmt.Errorf("missing recipe name")
		logger.Debug("recipe job: invalid start payload", "error", err)
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
	// Keep the load error separate from errors scoped to source resolution.
	var loadErr error
	if hasEmbeddedRecipeArtifact {
		r, loadErr = recipes.GetRecipe(input.RecipeName)
	} else {
		if j.rootResolver == nil {
			err = fmt.Errorf("recipe source resolver not configured to load non-artifact selector %q", input.RecipeName)
			logger.Debug("recipe job: failed to load recipe",
				"error", err,
			)
			return nil, err
		}
		req := rootSourceResolutionTaskInput{
			ProjectID: strings.TrimSpace(input.TenantId), Selector: input.RecipeName,
			LookupRepo: rootRecipeLookupRepo(input.JobContext), LookupRef: rootRecipeLookupRef(input.JobContext),
		}

		taskOutput, err := workflow.ValueCall(ctx, "root-source", RootSourceResolutionTaskType, jobdb.RunPolicy{}, req)
		if err != nil {
			if logReplayCacheMiss(logger, "recipe job: root recipe source resolution replay cache miss", err) {
				return nil, err
			}
			logger.Debug("recipe job: root recipe source resolution task failed",
				"error", err,
			)
			return nil, err
		}

		resolvedSource, err := ParseResolvedRecipeSourceTaskData(taskOutput)
		if err != nil {
			logger.Debug("recipe job: failed to decode root recipe source resolution output",
				"error", err,
			)
			return nil, err
		}
		resolution = resolvedSource.RecipeSourceResolution

		if strings.TrimSpace(resolvedSource.RecipeYAML) != "" {
			r, loadErr = resolvedSource.LoadRecipe()
		} else {
			r, loadErr = j.rootResolver.Load(context.Background(), strings.TrimSpace(input.TenantId), resolution)
		}
	}
	if loadErr == nil && r.RecipeImpl == nil {
		loadErr = fmt.Errorf("recipe source returned an empty recipe")
	}
	if loadErr != nil {
		logger.Debug("recipe job: failed to load recipe",
			"error", loadErr,
		)
		return nil, fmt.Errorf("load recipe %q: %w", input.RecipeName, loadErr)
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
		logger.Debug("recipe job: failed to index submitted artifacts",
			"error", err,
		)
		return nil, err
	}
	runContext.Artifacts = submittedRefs
	applyRootRecipeSource(&runContext, resolution)
	if err := ensureEnvironmentSentinels(&runContext.Environment); err != nil {
		logger.Debug("recipe job: invalid environment sentinel",
			"error", err,
		)
		return nil, err
	}
	err = ensureSentinel(&runContext.Workflow.JobID, contextual.JobIdSentinel, "job id")
	if err != nil {
		logger.Debug("recipe job: invalid job id sentinel",
			"error", err,
		)
		return nil, err
	}

	wCtx := workflow.Context{JobContext: ctx, BeforeScope: j.beforeScope, Logger: logger}
	if input.Execution != nil {
		wCtx.JobPackages = input.Execution.JobRequirements.Packages
	}
	if !j.readOnlyReplay {
		session, err := j.executionSession(ctx, r, input.Execution)
		if err != nil {
			return nil, err
		}
		wCtx.SuspendExecution = session.suspend
		wCtx.JobPackages = session.demand.JobRequirements.Packages
		if recipe.HasExecutionConstraints(r) {
			wCtx.StageNodeExecution = session.stageNode
		}
	}
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
		logger.Debug("recipe job: failed to create task data",
			"error", err,
		)
		return nil, err
	}
	logger.Debug("Recipe evaluation finished", "event", "recipe.evaluation.finished", "outcome", "succeeded", "read_only_replay", j.readOnlyReplay)
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
