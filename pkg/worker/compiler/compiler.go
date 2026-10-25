package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/input/formdefaults"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/toolenv"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// RecipeExecutor defines the surface area for executing recipes, states, and ops.
// This enables decorator implementations (e.g., analysis) without changing call sites.
type RecipeExecutor interface {
	ExecuteRecipe(ctx workflow.Context, r recipe.Recipe, rawRecipeInputs map[string]interface{}, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, opts ...ExecutionOptions) (map[string]interface{}, []jobdb.Artifact, error)
	ExecuteNode(ctx workflow.Context, parentResCtx *template.ResolutionContext, n *recipe.Node) error
	ExecuteStateMachine(ctx workflow.Context, parentContext *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, stateMap *recipe.StateMap, opts ...ExecutionOptions) error
	ExecuteOp(ctx workflow.Context, parentResolutionContext *template.ResolutionContext, metadata recipe.NodeMetadata, op string) error
	ExecuteChildGroup(ctx workflow.Context, parent *template.ResolutionContext, metadata recipe.NodeMetadata, group recipe.ChildGroupData) error
	ExecuteSequence(ctx workflow.Context, rCtx *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, sequence []recipe.Node) error
}

type StateObserver interface {
	StateEntered(stateName string)
	StateExited(stateName string)
	TransitionEvalauted(expression string, result bool, nextStateIfExpressionTrue string)
}

type TransitionSelectionObserver interface {
	TransitionSelected(fromState string, toState string, payload map[string]interface{})
}

type NoOpStateObserver struct{}

func (NoOpStateObserver) StateEntered(stateName string) {}
func (NoOpStateObserver) StateExited(stateName string)  {}
func (NoOpStateObserver) TransitionEvalauted(expression string, result bool, nextStateIfExpressionTrue string) {
}

// DefaultRecipeExecutor preserves the existing execution behavior, with optional CEL provider injection.
type DefaultRecipeExecutor struct {
	celProvider template.CELOptionsProvider
	delegate    RecipeExecutor
}

// NewDefaultRecipeExecutor builds an executor with a default CEL provider.
func NewDefaultRecipeExecutor(provider template.CELOptionsProvider) DefaultRecipeExecutor {
	return DefaultRecipeExecutor{celProvider: provider}
}

// WithDelegate returns a copy of the executor that will dispatch recursive executor calls
// (ExecuteNode/ExecuteSequence/ExecuteStateMachine/ExecuteOp) to the provided delegate.
// This allows decorators to wrap the default executor without reimplementing execution logic.
func (d DefaultRecipeExecutor) WithDelegate(delegate RecipeExecutor) DefaultRecipeExecutor {
	d.delegate = delegate
	return d
}

func (d DefaultRecipeExecutor) self() RecipeExecutor {
	if d.delegate != nil {
		return d.delegate
	}
	return d
}

// ExecuteRecipe keeps the existing public entry point, delegating to the default executor.
func ExecuteRecipe(ctx workflow.Context, r recipe.Recipe, rawRecipeInputs map[string]interface{}, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, opts ...ExecutionOptions) (map[string]interface{}, []jobdb.Artifact, error) {
	return DefaultRecipeExecutor{}.ExecuteRecipe(ctx, r, rawRecipeInputs, execCtx, commitContext, opts...)
}

// ExecuteRecipeWithExecutor allows callers to run with a custom executor (e.g., decorator).
func ExecuteRecipeWithExecutor(exec RecipeExecutor, ctx workflow.Context, r recipe.Recipe, rawRecipeInputs map[string]interface{}, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, opts ...ExecutionOptions) (map[string]interface{}, []jobdb.Artifact, error) {
	if exec == nil {
		exec = DefaultRecipeExecutor{}
	}
	return exec.ExecuteRecipe(ctx, r, rawRecipeInputs, execCtx, commitContext, opts...)
}

func (d DefaultRecipeExecutor) ExecuteRecipe(ctx workflow.Context, r recipe.Recipe, rawRecipeInputs map[string]interface{}, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, opts ...ExecutionOptions) (map[string]interface{}, []jobdb.Artifact, error) {

	jobKey := ctx.GetJobKey()
	execCtx.Workflow.JobID = jobKey.JobId
	execCtx.Workflow.ProjectId = jobKey.TenantId
	normalizeEnvironmentPathContext(&execCtx.Environment)

	// we forward thin packs from one task to the next to maintain state.
	forwarder := newThinPackForwardingJobContext(ctx.JobContext)
	forwarder.scopedMode = recipe.HasWorkspaces(r) || execCtx.Workspace != nil
	if execCtx.InitialCommit != nil {
		commitContext = *execCtx.InitialCommit
	}
	if execCtx.Workspace != nil {
		forwarder.rootScopeID = execCtx.Workspace.ScopeID
	}
	if execCtx.RestoreArtifact != nil {
		forwarder.initialRestore = map[string]*jobdb.ArtifactKey{forwarder.rootScopeID: execCtx.RestoreArtifact}
	}

	ctx.JobContext = forwarder
	ctx.WorkspaceSnapshots = true

	execOpts := normalizeExecutionOptions(opts)
	var err error
	ctx.RecipeDigest, err = recipe.ExecutionDigest(r)
	if err != nil {
		return nil, nil, err
	}
	if execOpts.CELOptionsProvider == nil && d.celProvider != nil {
		execOpts.CELOptionsProvider = d.celProvider
	}
	// CEL imports are fetched before the body. Resolve the root's profile in
	// the same caller-input context used by normal node entry.
	profileInputs, err := prepareRecipeInputs(r.GetMetdata(), rawRecipeInputs, execOpts)
	if err != nil {
		return nil, nil, err
	}
	profileContext, err := template.NewRecipeResolutionContext(&commitContext, profileInputs, execCtx, resolutionOptionsFromExecution(execOpts))
	if err != nil {
		return nil, nil, err
	}
	if err := profileContext.SelectGitProfile(r.GetMetadata().GitProfile, ""); err != nil {
		return nil, nil, err
	}
	execCtx.GitProfile = profileContext.GitProfile
	withinResolution, err := resolveWithinRecipeSelectors(ctx, r, execCtx, commitContext, execOpts)
	if err != nil {
		return nil, nil, err
	}
	execOpts.ResolvedSelectors = cloneResolvedSelectors(withinResolution.ResolvedSelectors)
	execOpts.ResolvedGitRefs = cloneResolvedGitRefs(withinResolution.ResolvedGitRefs)
	execOpts.CELOptionsProvider, err = recipeCELOptionsProvider(r, execCtx, commitContext, execOpts, execOpts.CELOptionsProvider)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to configure recipe extension functions: %w", err)
	}
	recipeInputs, err := prepareRecipeInputs(r.GetMetdata(), rawRecipeInputs, execOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("recipe inputs do not match schema. %w", err)
	}

	rCtx, err := template.NewRecipeResolutionContext(&commitContext, recipeInputs, execCtx, resolutionOptionsFromExecution(execOpts))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create resolution context: %w", err)
	}
	if execOpts.Mode == ExecutionModeValidate {
		if rCtx.Options.ResolvedGitRefs == nil {
			rCtx.Options.ResolvedGitRefs = map[string]string{}
		}
		if rCtx.Options.ResolvedSelectors == nil {
			rCtx.Options.ResolvedSelectors = map[string]string{}
		}
		ctx = wrapValidationContext(ctx, commitContext, rCtx.Options.ResolvedSelectors, rCtx.Options.ResolvedGitRefs)
	}

	if base := r.GetMetdata().Execution; base != nil && len(base.Packages) > 0 {
		rCtx.ToolScopes = append(rCtx.ToolScopes, toolenv.Scope{ID: "recipe", Packages: base.Packages})
	}
	if ctx.JobPackages != nil {
		// Keep the job scope's precedence while refreshing its packages for each
		// live step from directives reconstructed during replay.
		rCtx.ToolScopes = append(rCtx.ToolScopes, toolenv.Scope{ID: "job"})
	}

	metadata := r.GetMetadata().NodeMetadata
	if recipe.HasPermissions(r) {
		rCtx.Permissions.Version = permissions.Version
	}
	if err := rCtx.SelectGitProfile(metadata.GitProfile, ""); err != nil {
		return nil, nil, err
	}
	body := func(inner workflow.Context, scoped *template.ResolutionContext, meta recipe.NodeMetadata) error {
		if err := scoped.ResolveVars(meta.Vars); err != nil {
			return fmt.Errorf("failed to resolve recipe vars: %w", err)
		}
		if execOpts.Mode == ExecutionModeValidate {
			if err := validateCatchSemantics(r, scoped); err != nil {
				return err
			}
		}
		meta.Vars = nil
		switch t := r.RecipeImpl.(type) {
		case *recipe.RecipeState:
			return d.self().ExecuteStateMachine(inner, scoped, meta, t.Outputs, t.StateMachineData.States, execOpts)
		case *recipe.RecipeOp:
			return d.self().ExecuteOp(inner, scoped, meta, t.OpData.Op)
		case *recipe.RecipeSequence:
			return d.self().ExecuteSequence(inner, scoped, meta, t.Outputs, t.SequenceData.Sequence)
		case *recipe.RecipeChildGroup:
			return d.self().ExecuteChildGroup(inner, scoped, meta, t.ChildGroup)
		default:
			return fmt.Errorf("unsupported recipe type: %T", t)
		}
	}
	if metadata.Workspace != nil {
		err = withNodeWorkspace(ctx, rCtx, metadata, body)
	} else {
		err = body(ctx, rCtx, metadata)
	}
	if err != nil {
		return nil, nil, err
	}
	return rCtx.GetLastExecution(), forwarder.resultArtifacts(rCtx.GetLastArtifacts()), nil
}

func normalizeEnvironmentPathContext(env *contextual.EnvironmentContext) {
	if env == nil {
		return
	}

	normalizeHostPathAlias(&env.WorktreePath, &env.Host.WorktreePath, contextual.WorktreePathSentinel)
	normalizeHostPathAlias(&env.WorkdirPath, &env.Host.Workdir, contextual.WorkdirPathSentinel)
	normalizeHostPathAlias(&env.ArtifactInbox, &env.Host.Inbox, contextual.ArtifactInboxSentinel)
	normalizeHostPathAlias(&env.ArtifactOutbox, &env.Host.Outbox, contextual.ArtifactOutboxSentinel)

	normalizeOpPathAlias(&env.Op.WorktreePath, env.Host.WorktreePath, contextual.WorktreePathSentinel, contextual.OpWorktreePathSentinel)
	normalizeOpPathAlias(&env.Op.Workdir, env.Host.Workdir, contextual.WorkdirPathSentinel, contextual.OpWorkdirPathSentinel)
	normalizeOpPathAlias(&env.Op.Inbox, env.Host.Inbox, contextual.ArtifactInboxSentinel, contextual.OpArtifactInboxSentinel)
	normalizeOpPathAlias(&env.Op.Outbox, env.Host.Outbox, contextual.ArtifactOutboxSentinel, contextual.OpArtifactOutboxSentinel)
}

func normalizeHostPathAlias(flat *string, host *string, sentinel string) {
	flatValue := strings.TrimSpace(*flat)
	hostValue := strings.TrimSpace(*host)
	if flatValue == "" && hostValue != "" {
		*flat = hostValue
		flatValue = hostValue
	}
	if flatValue == "" {
		*flat = sentinel
		flatValue = sentinel
	}
	if hostValue == "" {
		*host = flatValue
	}
}

func normalizeOpPathAlias(op *string, host string, hostSentinel string, opSentinel string) {
	if strings.TrimSpace(*op) != "" {
		return
	}
	host = strings.TrimSpace(host)
	if host == "" || host == hostSentinel {
		*op = opSentinel
		return
	}
	*op = host
}

// ExecuteWorkflow implements the WorkflowExecutor interface for unified recipes
func (d DefaultRecipeExecutor) ExecuteNode(ctx workflow.Context, parentResCtx *template.ResolutionContext, n *recipe.Node) error {
	metadata := n.GetMetadata()
	if metadata.Internal != nil && metadata.Internal.Inline != nil && metadata.Internal.Inline.Execution != nil && !metadata.Internal.Inline.Execution.Empty() && ctx.UpdateExecution != nil {
		patch := *metadata.Internal.Inline.Execution
		patch.Packages = nil
		if !patch.Empty() {
			if err := ctx.UpdateExecution(patch); err != nil {
				return err
			}
		}
	}
	switch t := n.NodeImpl.(type) {
	case *recipe.NodeState:
		return d.self().ExecuteStateMachine(ctx, parentResCtx, metadata, t.Outputs, t.StateMachineData.States)
	case *recipe.NodeOp:
		return d.self().ExecuteOp(ctx, parentResCtx, metadata, t.OpData.Op)
	case *recipe.NodeSequence:
		return d.self().ExecuteSequence(ctx, parentResCtx, metadata, t.Outputs, t.SequenceData.Sequence)
	case *recipe.NodeChildGroup:
		return d.self().ExecuteChildGroup(ctx, parentResCtx, metadata, t.ChildGroup)
	case *recipe.NodeInclude:
		return fmt.Errorf("include node %q reached execution; resolve inline recipes before execution", template.ScopeID(metadata, "", template.ScopeSequence))
	default:
		return fmt.Errorf("unsupported recipe type: %T", t)
	}
}

// StepResult stores the result of a workflow step
type StepResult struct {
	Outputs map[string]interface{}
}

func (d DefaultRecipeExecutor) ExecuteOp(ctx workflow.Context, parentResolutionContext *template.ResolutionContext, metadata recipe.NodeMetadata, op string) error {
	if registered, ok := ops.Get(op); ok && metadata.Timeout == 0 {
		metadata.Timeout = recipe.Duration(effectiveOpTimeout(metadata, registered))
	}
	if metadata.Timeout == 0 && (parentResolutionContext.Permissions.Version != 0 || metadata.Perms != nil) {
		metadata.Timeout = recipe.Duration(time.Hour)
	}
	return executeNodeScope(ctx, parentResolutionContext, metadata, template.ScopeOp, op, func(inner workflow.Context, state *template.ResolutionContext, meta recipe.NodeMetadata) error {
		return d.executeOpBody(inner, state, meta, op)
	})
}
func (d DefaultRecipeExecutor) executeOpBody(ctx workflow.Context, parentResolutionContext *template.ResolutionContext, metadata recipe.NodeMetadata, op string) error {
	if metadata.Workspace != nil {
		return withNodeWorkspace(ctx, parentResolutionContext, metadata, func(inner workflow.Context, scoped *template.ResolutionContext, meta recipe.NodeMetadata) error {
			return d.executeOpBody(inner, scoped, meta, op)
		})
	}
	// Evaluation also runs during replay; only task workers can report execution.
	return d.executeOpAttempt(ctx, parentResolutionContext, metadata, op)
}

// executeOperation executes a single operation node attempt.
func (d DefaultRecipeExecutor) executeOpAttempt(ctx workflow.Context, parentResolutionContext *template.ResolutionContext, metadata recipe.NodeMetadata, op string) error {
	if metadata.Inputs == nil {
		metadata.Inputs = map[string]interface{}{}
	}
	artifactsDefined := metadata.Artifacts != nil
	if metadata.Artifacts == nil {
		metadata.Artifacts = map[string]interface{}{}
	}

	resCtx, err := parentResolutionContext.NewChildContext(template.ScopeOp, metadata, op, nil)
	if err != nil {
		return fmt.Errorf("failed to create resolution context: %w", err)
	}
	if ctx.ScopeState != nil {
		ctx.ScopeState(resCtx)
	}
	if err := resCtx.ResolveVars(metadata.Vars); err != nil {
		return fmt.Errorf("failed to resolve op vars: %w", err)
	}
	if err := resCtx.ResolveExecutionNeeds(metadata.ExecutionNeeds); err != nil {
		return err
	}

	var extensionRequest *workerops.ExtensionResolutionRequest
	var (
		registeredOp ops.RegisterableOp
		chain        []ops.TaskStep
		taskPrefix   string
		selectorOp   interface {
			ApplyInvocationDefaults(map[string]interface{}) (bool, error)
			ValidateInvocationInputs(map[string]interface{}) error
		}
	)
	if isSelectorOp(op) {
		pinnedSelector := resolvedSelector(op, resCtx.Options.ResolvedSelectors)
		resolveOpts := selectorLoadResolveOptions(resCtx.TaskExecutionContext().JobContext(), resCtx.GetGitCommitContext())
		resolveOpts.ResolvedRefs = cloneResolvedGitRefs(resCtx.Options.ResolvedGitRefs)
		snapshot, err := resCtx.InputSnapshot()
		if err != nil {
			return err
		}
		extensionRequest = &workerops.ExtensionResolutionRequest{Selector: pinnedSelector, Options: resolveOpts, TemplateContext: snapshot, RawInputs: metadata.Inputs}
		var exists bool
		registeredOp, exists = ops.Get(extops.ExecutionOpType)
		if !exists {
			return fmt.Errorf("extension execution op not registered")
		}
		chain = registeredOp.TaskChain()
		taskPrefix = registeredOp.GetMetadata().Type
		if resCtx.Options.Mode == template.ModeValidate {
			resolved, _, err := loadSelectorOp(pinnedSelector, resolveOpts)
			if err != nil {
				return err
			}
			selectorOp = resolved
			if _, err := selectorOp.ApplyInvocationDefaults(metadata.Inputs); err != nil {
				return err
			}
		}
	} else {
		var exists bool
		registeredOp, exists = ops.Get(op)
		if !exists {
			return fmt.Errorf("operation %s not found", op)
		}
		if artifactsDefined && !registeredOp.GetMetadata().AcceptsArtifacts {
			return fmt.Errorf("operation %s does not accept artifacts", op)
		}
		chain = registeredOp.TaskChain()
		taskPrefix = registeredOp.GetMetadata().Type

	}

	resolveNormalizedInputs := func(resCtx *template.ResolutionContext, inputs map[string]interface{}) (map[string]interface{}, []jobdb.ArtifactKey, error) {
		resolvedNodeInputs, err := resCtx.ResolveInputs(inputs)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to resolve templates op inputs: %w", err)
		}

		if extensionRequest != nil {
			if selectorOp != nil && !configuration.Contains(resolvedNodeInputs) {
				if err := selectorOp.ValidateInvocationInputs(resolvedNodeInputs); err != nil {
					return nil, nil, fmt.Errorf("failed to validate selector inputs: %w", err)
				}
			}
			invocationRepoSource, invocationRepoRef := selectorInvocationRepository(resCtx.TaskExecutionContext())
			normalizedInput, err := NormalizeOpInput(chain[0].InputType, selectorInvocationInput(
				resolvedSelector(op, resCtx.Options.ResolvedSelectors),
				resolvedNodeInputs,
				invocationRepoSource,
				invocationRepoRef,
			))
			if err != nil {
				return nil, nil, fmt.Errorf("failed to normalize selector op inputs: %w", err)
			}
			allowNulls := resCtx.Options.Mode == template.ModeValidate
			if err := validateOpInputType(chain[0].InputType, normalizedInput.Data, allowNulls); err != nil {
				return nil, nil, fmt.Errorf("op input validation failed: %w", err)
			}
			return normalizedInput.Data, normalizedInput.StoredArtifactKeys, nil
		}

		normalizedInput, err := NormalizeOpInput(chain[0].InputType, resolvedNodeInputs)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to normalize op inputs: %w", err)
		}

		allowNulls := resCtx.Options.Mode == template.ModeValidate
		if err := validateOpInputType(chain[0].InputType, normalizedInput.Data, allowNulls); err != nil {
			return nil, nil, fmt.Errorf("op input validation failed: %w", err)
		}
		return normalizedInput.Data, normalizedInput.StoredArtifactKeys, nil
	}

	var stepInput, initialStepInput map[string]interface{}
	// Execute the operation
	retry := jobdb.RetryPolicy{MaximumAttempts: 1}
	if metadata.Retry != nil {
		retry = *metadata.Retry
	}

	runPolicy := jobdb.RunPolicy{
		Retry: retry,
	}
	taskType := fmt.Sprintf("%s:%s", taskPrefix, chain[0].Name)
	var taskAlternate *jobworkflow.TaskAlternateAfter

	var stepArtifacts map[string]recipeartifacts.Ref
	var stepOutputArtifacts []jobdb.Artifact
	var stepJobs jobcontext.StartedJobsContext
	for i := 0; i < 64; i++ { // guard against accidental loops
		done := false
		{

			commandInput := map[string]any{"recipe": ctx.RecipeDigest, "node": resCtx.TaskExecutionContext().Invocation.NodePath, "op": op, "inputs": metadata.Inputs, "step": taskType, "artifacts": metadata.Artifacts}
			if i > 0 {
				commandInput["prior"] = stepInput
			}
			command, err := json.Marshal(commandInput)
			if err != nil {
				return err
			}
			if ctx.StageNodeExecution != nil {
				ctx.StageNodeExecution(resCtx.ExecutionNeeds)
			}
			out, err := ctx.DoTask(jobworkflow.TaskCall{Site: taskType, TaskType: taskType, ContractVersion: 1, Command: command, Policy: runPolicy, Alternate: taskAlternate}, func(buildContext context.Context) (jobdb.TaskData, error) {
				buildResolver, err := resCtx.CloneAttempt()
				if err != nil {
					return nil, err
				}
				var artifactKeys []jobdb.ArtifactKey
				input := stepInput
				if i == 0 {
					raw, e := json.Marshal(metadata.Inputs)
					if e != nil {
						return nil, e
					}
					var declared map[string]interface{}
					if e = json.Unmarshal(raw, &declared); e != nil {
						return nil, e
					}
					if extensionRequest == nil && len(chain) > 0 {
						if e = workerops.InjectDefaults(chain[0].InputType, declared); e != nil {
							return nil, e
						}
					}
					input, artifactKeys, e = resolveNormalizedInputs(buildResolver, declared)
					if e != nil {
						return nil, e
					}
				}
				resolvedArtifacts, e := resolveArtifactBindings(buildResolver, map[string]interface{}(metadata.Artifacts))
				if e != nil {
					return nil, e
				}
				artifactKeys = appendArtifactKeys(artifactKeys, resolvedArtifacts)
				// Later steps can introduce references that were not in the original op
				// inputs (for example, a child-result checkpoint in an await chain).
				invocationKeys := artifactKeys
				refs, err := objects.Collect(input)
				if err != nil {
					return nil, fmt.Errorf("object input: %w", err)
				}
				if len(refs) > 0 {
					acc := newArtifactKeyAccumulator()
					for _, key := range artifactKeys {
						if err := acc.Add(key); err != nil {
							return nil, err
						}
					}
					for _, ref := range refs {
						if err := acc.Add(ref.Artifact); err != nil {
							return nil, err
						}
					}
					invocationKeys = acc.Keys()
				}
				preparation := workerops.PreparationRequest{Extension: extensionRequest}
				for _, scope := range resCtx.ToolScopes {
					if scope.ID == "job" && ctx.JobPackages != nil {
						scope.Packages = append([]string(nil), ctx.JobPackages()...)
						if len(scope.Packages) == 0 {
							continue
						}
					}
					preparation.Scopes = append(preparation.Scopes, scope)
				}
				invocation := workerops.ActivityInvocationRequest{
					GitProfile:     resCtx.GitProfile,
					Preparation:    preparation,
					Input:          input,
					Const:          resCtx.EffectiveConst,
					GitTaskContext: *gitstate.NewGlobalGitTaskContext(resCtx.TaskExecutionContext()),
					ArtifactKeys:   invocationKeys,
					Artifacts:      resolvedArtifacts,
				}
				if resCtx.Permissions.Version != 0 {
					p := resCtx.Permissions
					invocation.Permissions = &p
				}

				taskData, err := jobdb.NewTaskData(invocation)
				if err != nil {
					return nil, err
				}

				if err := buildContext.Err(); err != nil {
					return nil, err
				}
				return taskData, nil
			})

			if err != nil {
				return newRecipeFailureError(normalizeRuntimeFailure(err, resCtx, metadata, recipe.FailureNodeOp, op), err)
			}

			outputData, err := out.GetData()
			if err != nil {
				return err
			}

			decoded, err := decodeTaskOutput(outputData)
			if err != nil {
				return err
			}

			switch decoded.Kind {
			case coretask.OutputKindActivityInvocationOutput:
				if ws := resCtx.TaskExecutionContext().Workspace; ws != nil && ws.ScopeID != "" && resCtx.Options.Mode != template.ModeValidate && decoded.Activity.WorkspaceScopeID != ws.ScopeID {
					return fmt.Errorf("activity result belongs to workspace %q, expected %q", decoded.Activity.WorkspaceScopeID, ws.ScopeID)
				}
				resCtx.UpdateGitState(decoded.Activity.GitResult)
				if decoded.Activity.Execution != nil && ctx.UpdateExecution != nil {
					if err := ctx.UpdateExecution(*decoded.Activity.Execution); err != nil {
						return err
					}
				}
				stepInput = decoded.Activity.OpOutput
				for _, step := range chain {
					if taskType == taskPrefix+":"+step.Name {
						stepInput = normalizeOpOutput(step.OutputType, stepInput)
						break
					}
				}
				if taskPrefix == "input" && i == 0 && decoded.Activity.NextTask != "" {
					initialStepInput = map[string]interface{}{"form": decoded.Activity.OpOutput, "if_unanswered": decoded.Activity.OpOutput["fallback"]}
				}
				var mergeErr error
				stepJobs, mergeErr = workerops.MergeStartedJobs(stepJobs, decoded.Activity.Jobs)
				if mergeErr != nil {
					return mergeErr
				}
				if decoded.Activity.NextTask == "" {
					outputArtifacts, err := out.GetArtifacts()
					if err != nil {
						return err
					}
					resCtx.RememberArtifacts(outputArtifacts)
					stepOutputArtifacts = append([]jobdb.Artifact(nil), outputArtifacts...)
					stepArtifacts = mergeArtifactRefs(artifactsToMap(outputArtifacts), decoded.Activity.ArtifactRefs)
					done = true
				} else {
					taskType = decoded.Activity.NextTask
					taskAlternate = decoded.Activity.NextTaskAlternate
				}
				break

			default:
				return fmt.Errorf("unsupported task output kind: %q", decoded.Kind)
			}
		}
		if done {
			break
		}
	}
	if taskPrefix == "input" {
		normalizedOutput, err := formdefaults.NormalizeOutputMap(initialStepInput, stepInput)
		if err != nil {
			return err
		}
		stepInput = normalizedOutput
	}
	resCtx.AddExecutionWithContextData(stepInput, stepArtifacts, stepOutputArtifacts, stepJobs)
	return nil
}

func (d DefaultRecipeExecutor) innerSequence(ctx workflow.Context, parentCtx *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, sequence []recipe.Node) error {
	// Create resolution context for this sequence
	resolvedInputs, err := parentCtx.ResolveCompositeInputs(metadata.Inputs)
	if err != nil {
		return fmt.Errorf("failed to resolve sequence inputs: %w", err)
	}
	resolvedInputs, err = prepareCompositeInputs(metadata, resolvedInputs)
	if err != nil {
		return fmt.Errorf("sequence inputs do not match schema. %w", err)
	}

	resCtx, err := parentCtx.NewChildContext(template.ScopeSequence, metadata, "", resolvedInputs)
	if err != nil {
		return fmt.Errorf("failed to create resolution context: %w", err)
	}
	if ctx.ScopeState != nil {
		ctx.ScopeState(resCtx)
	}
	if err := resCtx.ResolveVars(metadata.Vars); err != nil {
		return fmt.Errorf("failed to resolve sequence vars: %w", err)
	}
	if err := resCtx.ResolveExecutionNeeds(metadata.ExecutionNeeds); err != nil {
		return err
	}
	if err := seedSequencePlaceholders(resCtx, sequence); err != nil {
		return err
	}

	for i, node := range sequence {
		// Execute the node
		err := d.self().ExecuteNode(ctx, resCtx, &node)
		if isExecutionControlError(err) {
			return err
		}
		if err != nil {
			var routeErr *catchRouteError
			if errors.As(err, &routeErr) {
				return routeErr
			}
			failure, ok := failureFromError(err)
			if !ok {
				failure = normalizeRuntimeFailure(err, resCtx, metadata, recipe.FailureNodeSequence, "")
			}
			wrapped := fmt.Errorf("sequence node %d failed: %w", i, err)
			return newRecipeFailureError(failure, wrapped)
		}
	}

	outputs, err := resCtx.ResolveMap(outputTemplate)
	if err != nil {
		return fmt.Errorf("failed to resolve sequence outputs: %w", err)
	}

	// add resolved output to parent context.
	resCtx.AddExecutionWithArtifactData(outputs, lastSequenceArtifacts(resCtx, sequence), resCtx.GetLastArtifacts())
	return nil
}

func (d DefaultRecipeExecutor) ExecuteSequence(ctx workflow.Context, rCtx *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, sequence []recipe.Node) error {
	return executeNodeScope(ctx, rCtx, metadata, template.ScopeSequence, "", func(inner workflow.Context, state *template.ResolutionContext, meta recipe.NodeMetadata) error {
		if meta.Workspace != nil {
			return withNodeWorkspace(inner, state, meta, func(inner workflow.Context, state *template.ResolutionContext, meta recipe.NodeMetadata) error {
				return d.innerSequence(inner, state, meta, outputTemplate, sequence)
			})
		}
		return d.innerSequence(inner, state, meta, outputTemplate, sequence)
	})
}

func effectiveOpTimeout(metadata recipe.NodeMetadata, registeredOp ops.RegisterableOp) time.Duration {
	if metadata.Timeout > 0 {
		return time.Duration(metadata.Timeout)
	}
	if registeredOp == nil {
		return 0
	}
	return registeredOp.GetMetadata().DefaultTimeout
}
