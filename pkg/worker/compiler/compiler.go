package compiler

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/input/formdefaults"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/template"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/swf-go/pkg/swf"

	"github.com/colony-2/c2j/pkg/workflow"
)

// RecipeExecutor defines the surface area for executing recipes, states, and ops.
// This enables decorator implementations (e.g., analysis) without changing call sites.
type RecipeExecutor interface {
	ExecuteRecipe(ctx workflow.Context, r recipe.Recipe, rawRecipeInputs map[string]interface{}, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, opts ...ExecutionOptions) (map[string]interface{}, []swf.Artifact, error)
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
func ExecuteRecipe(ctx workflow.Context, r recipe.Recipe, rawRecipeInputs map[string]interface{}, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, opts ...ExecutionOptions) (map[string]interface{}, []swf.Artifact, error) {
	return DefaultRecipeExecutor{}.ExecuteRecipe(ctx, r, rawRecipeInputs, execCtx, commitContext, opts...)
}

// ExecuteRecipeWithExecutor allows callers to run with a custom executor (e.g., decorator).
func ExecuteRecipeWithExecutor(exec RecipeExecutor, ctx workflow.Context, r recipe.Recipe, rawRecipeInputs map[string]interface{}, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, opts ...ExecutionOptions) (map[string]interface{}, []swf.Artifact, error) {
	if exec == nil {
		exec = DefaultRecipeExecutor{}
	}
	return exec.ExecuteRecipe(ctx, r, rawRecipeInputs, execCtx, commitContext, opts...)
}

func (d DefaultRecipeExecutor) ExecuteRecipe(ctx workflow.Context, r recipe.Recipe, rawRecipeInputs map[string]interface{}, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, opts ...ExecutionOptions) (map[string]interface{}, []swf.Artifact, error) {

	jobKey := ctx.GetJobKey()
	execCtx.Workflow.JobID = jobKey.JobId
	execCtx.Workflow.ProjectId = jobKey.TenantId
	normalizeEnvironmentPathContext(&execCtx.Environment)

	// we forward thin packs from one task to the next to maintain state.
	ctx.JobContext = newThinPackForwardingJobContext(ctx.JobContext)

	execOpts := normalizeExecutionOptions(opts)
	var err error
	if execOpts.CELOptionsProvider == nil && d.celProvider != nil {
		execOpts.CELOptionsProvider = d.celProvider
	}
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

	if execOpts.Mode == ExecutionModeValidate {
		ctx = wrapValidationContext(ctx, commitContext)
	}

	rCtx, err := template.NewRecipeResolutionContext(&commitContext, recipeInputs, execCtx, resolutionOptionsFromExecution(execOpts))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create resolution context: %w", err)
	}

	metadata := r.GetMetadata().NodeMetadata
	if err := rCtx.ResolveVars(metadata.Vars); err != nil {
		return nil, nil, fmt.Errorf("failed to resolve recipe vars: %w", err)
	}
	if execOpts.Mode == ExecutionModeValidate {
		if err := validateCatchSemantics(r, rCtx); err != nil {
			return nil, nil, err
		}
	}
	rootMetadata := metadata
	rootMetadata.Vars = nil

	switch t := r.RecipeImpl.(type) {
	case *recipe.RecipeState:
		err = d.self().ExecuteStateMachine(ctx, rCtx, rootMetadata, t.Outputs, t.StateMachineData.States, execOpts)
	case *recipe.RecipeOp:
		err = d.self().ExecuteOp(ctx, rCtx, rootMetadata, t.OpData.Op)
	case *recipe.RecipeSequence:
		err = d.self().ExecuteSequence(ctx, rCtx, rootMetadata, t.Outputs, t.SequenceData.Sequence)
	case *recipe.RecipeChildGroup:
		err = d.self().ExecuteChildGroup(ctx, rCtx, rootMetadata, t.ChildGroup)
	default:
		return nil, nil, fmt.Errorf("unsupported recipe type: %T", t)
	}

	if err != nil {
		return nil, nil, err
	}
	return rCtx.GetLastExecution(), rCtx.GetLastArtifacts(), nil
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
	l := slog.Default()
	l.Info("executing op", "op", op)
	err := d.executeOp2(ctx, parentResolutionContext, metadata, op)
	if err != nil {
		if logReplayCacheMiss(l, "op execution replay cache miss", err, "op", op) {
			return err
		}
		l.Error("failed to execute op", "op", op, "err", err)
		return err
	}

	l.Info("op executed successfully", "op", op)
	return nil
}

func (d DefaultRecipeExecutor) executeOp2(ctx workflow.Context, parentResolutionContext *template.ResolutionContext, metadata recipe.NodeMetadata, op string) error {
	catchAware := len(metadata.Catch) > 0 || parentResolutionContext.Options.CatchBeforeRetry
	if !catchAware {
		return d.executeOpAttempt(ctx, parentResolutionContext, metadata, op)
	}

	attemptMetadata := metadata
	singleAttempt := singleAttemptRetryPolicy(metadata.Retry)
	attemptMetadata.Retry = &singleAttempt
	attempts := 1
	if len(metadata.Catch) > 0 {
		attempts = retryPolicyAttempts(metadata.Retry)
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		err := d.executeOpAttempt(ctx, parentResolutionContext, attemptMetadata, op)
		if err == nil {
			return nil
		}
		failure, ok := failureFromError(err)
		if !ok {
			return err
		}

		if len(metadata.Catch) > 0 {
			decision, catchErr := evaluateCatchClauses(metadata.Catch, failure, parentResolutionContext, containingStateName(parentResolutionContext), canRouteToState(parentResolutionContext))
			if catchErr != nil {
				return catchErr
			}
			switch decision.Kind {
			case catchDecisionRoute:
				return &catchRouteError{Transition: template.NewFailureTransitionData(containingStateName(parentResolutionContext), decision.To, decision.Payload, decision.Failure)}
			case catchDecisionContinue:
				return recordSyntheticNodeOutput(parentResolutionContext, template.ScopeOp, metadata, op, decision.Outputs)
			case catchDecisionFail:
				err = decision.Error
				failure = decision.Failure
			}
		}

		if attempt < attempts && shouldRetryFailure(err, failure, metadata.Retry) {
			if delay := retryDelay(metadata.Retry, attempt); delay > 0 {
				if awaitErr := ctx.JobContext.AwaitDuration(swf.Duration(delay)); awaitErr != nil {
					return awaitErr
				}
			}
			continue
		}
		return newRecipeFailureError(failure, err)
	}
	return nil
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
	if err := resCtx.ResolveVars(metadata.Vars); err != nil {
		return fmt.Errorf("failed to resolve op vars: %w", err)
	}

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
		resolvedSelectorOp, selectorRegisteredOp, err := loadSelectorOp(pinnedSelector, resolveOpts)
		if err != nil {
			return err
		}
		registeredOp = selectorRegisteredOp
		chain = registeredOp.TaskChain()
		taskPrefix = registeredOp.GetMetadata().Type
		selectorOp = resolvedSelectorOp
		if _, err := selectorOp.ApplyInvocationDefaults(metadata.Inputs); err != nil {
			return fmt.Errorf("failed to apply selector input defaults: %w", err)
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
		if len(chain) > 0 {
			if err := workerops.InjectDefaults(chain[0].InputType, metadata.Inputs); err != nil {
				return fmt.Errorf("failed to inject defaults: %w", err)
			}
		}
	}

	resolvedArtifacts, err := resolveArtifactBindings(resCtx, map[string]interface{}(metadata.Artifacts))
	if err != nil {
		return err
	}

	resolveNormalizedInputs := func() (map[string]interface{}, []swf.ArtifactKey, error) {
		resolvedNodeInputs, err := resCtx.ResolveMap(metadata.Inputs)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to resolve templates op inputs: %w", err)
		}

		if selectorOp != nil {
			if err := selectorOp.ValidateInvocationInputs(resolvedNodeInputs); err != nil {
				return nil, nil, fmt.Errorf("failed to validate selector inputs: %w", err)
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

	stepInput, artifactKeys, err := resolveNormalizedInputs()
	if err != nil {
		return err
	}
	initialStepInput := cloneStringMap(stepInput)
	if len(resolvedArtifacts) > 0 {
		artifactKeys = appendArtifactKeys(artifactKeys, resolvedArtifacts)
	}

	// Execute the operation
	retry := swf.RetryPolicy{}
	if metadata.Retry != nil {
		retry = *metadata.Retry
	}

	runPolicy := swf.RunPolicy{
		Retry: retry,
	}
	if opTimeout := effectiveOpTimeout(metadata, registeredOp); opTimeout > 0 {
		ctx.JobContext = withExecutionTimeout(ctx.JobContext, opTimeout, fmt.Sprintf("op %q", op))
	}
	taskExecutionTimeout := activeExecutionTimeoutLimit(ctx.JobContext)
	if taskExecutionTimeout > 0 {
		totalTimeout := swf.Duration(taskExecutionTimeout)
		runPolicy.TotalTimeout = &totalTimeout
	}

	taskType := fmt.Sprintf("%s:%s", taskPrefix, chain[0].Name)

	var stepArtifacts map[string]recipeartifacts.Ref
	var stepOutputArtifacts []swf.Artifact
	for i := 0; i < 64; i++ { // guard against accidental loops
		done := false
		for patchAttempts := 0; patchAttempts < 64; patchAttempts++ {
			invocation := workerops.ActivityInvocationRequest{
				Input:          stepInput,
				Const:          resCtx.EffectiveConst,
				GitTaskContext: *gitstate.NewGlobalGitTaskContext(resCtx.TaskExecutionContext()),
				ArtifactKeys:   artifactKeys,
				Artifacts:      resolvedArtifacts,
			}

			taskData, err := swf.NewTaskData(invocation)
			if err != nil {
				return err
			}

			out, err := ctx.DoTask(
				runPolicy,
				taskType,
				taskData,
			)

			mismatchErr := err
			hadMismatch := false
			if err != nil {
				if mismatch, ok := swf.UnexpectedChapter(err); ok {
					if mismatch.CachedTaskDataErr() != nil {
						return fmt.Errorf("rehydrate cached task output: %w", mismatch.CachedTaskDataErr())
					}
					out = mismatch.CachedTaskData()
					hadMismatch = true
				} else {
					failure := normalizeRuntimeFailure(err, resCtx, metadata, recipe.FailureNodeOp, op)
					return newRecipeFailureError(failure, err)
				}
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
				if hadMismatch {
					// A mismatch that still produced an activity output indicates real non-determinism.
					return mismatchErr
				}
				resCtx.UpdateGitState(decoded.Activity.GitResult)
				stepInput = normalizeOpOutput(chain[i].OutputType, decoded.Activity.OpOutput)
				if decoded.Activity.NextTask == "" {
					outputArtifacts, err := out.GetArtifacts()
					if err != nil {
						return err
					}
					resCtx.RememberArtifacts(outputArtifacts)
					stepOutputArtifacts = append([]swf.Artifact(nil), outputArtifacts...)
					stepArtifacts = mergeArtifactRefs(artifactsToMap(outputArtifacts), decoded.Activity.ArtifactRefs)
					done = true
				} else {
					taskType = decoded.Activity.NextTask
				}
				break

			case coretask.OutputKindContextPatch:
				if err := resCtx.ApplyContextPatch(decoded.Patch); err != nil {
					return fmt.Errorf("apply context patch: %w", err)
				}

				// Re-resolve inputs for the task based on the updated context.
				if i == 0 {
					stepInput, artifactKeys, err = resolveNormalizedInputs()
					if err != nil {
						return err
					}
					initialStepInput = cloneStringMap(stepInput)
				}

				// Re-resolve artifacts and keys (patch may have changed bindings or inputs).
				resolvedArtifacts, err = resolveArtifactBindings(resCtx, map[string]interface{}(metadata.Artifacts))
				if err != nil {
					return err
				}
				if len(resolvedArtifacts) > 0 {
					artifactKeys = appendArtifactKeys(artifactKeys, resolvedArtifacts)
				}
				continue

			default:
				return fmt.Errorf("unsupported task output kind: %q", decoded.Kind)
			}
			break
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
	resCtx.AddExecutionWithArtifactData(stepInput, stepArtifacts, stepOutputArtifacts)
	return nil
}

func (d DefaultRecipeExecutor) innerSequence(ctx workflow.Context, parentCtx *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, sequence []recipe.Node) error {
	// Create resolution context for this sequence
	resolvedInputs, err := parentCtx.ResolveMap(metadata.Inputs)
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
	if err := resCtx.ResolveVars(metadata.Vars); err != nil {
		return fmt.Errorf("failed to resolve sequence vars: %w", err)
	}
	if len(metadata.Catch) > 0 {
		resCtx.Options.CatchBeforeRetry = true
	}
	if err := seedSequencePlaceholders(resCtx, sequence); err != nil {
		return err
	}

	for i, node := range sequence {
		// Execute the node
		err := d.self().ExecuteNode(ctx, resCtx, &node)
		if err != nil {
			var routeErr *catchRouteError
			if errors.As(err, &routeErr) {
				return routeErr
			}
			failure, ok := failureFromError(err)
			if !ok {
				failure = normalizeRuntimeFailure(err, resCtx, metadata, recipe.FailureNodeSequence, "")
			}
			if len(metadata.Catch) > 0 {
				decision, catchErr := evaluateCatchClauses(metadata.Catch, failure, resCtx, containingStateName(resCtx), canRouteToState(resCtx))
				if catchErr != nil {
					return catchErr
				}
				switch decision.Kind {
				case catchDecisionRoute:
					return &catchRouteError{Transition: template.NewFailureTransitionData(containingStateName(resCtx), decision.To, decision.Payload, decision.Failure)}
				case catchDecisionContinue:
					resCtx.AddExecutionWithArtifactData(decision.Outputs, nil, nil)
					return nil
				case catchDecisionFail:
					return decision.Error
				}
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
	timeout := time.Duration(metadata.Timeout)
	fn := func(inner workflow.Context) error {
		e := d.innerSequence(inner, rCtx, metadata, outputTemplate, sequence)
		return e
	}
	err := executeCompositeInEnvelope(ctx, metadata.Retry, timeout, fmt.Sprintf("sequence %q", template.ScopeID(metadata, "", template.ScopeSequence)), fn)

	return err
}

// executeCompositeInEnvelope executes a composite nodes in a retry/timeout envelope
func executeCompositeInEnvelope(ctx workflow.Context, retry *recipe.RetryPolicy, timeoutDuration time.Duration, label string, fn func(inner workflow.Context) error) error {
	if timeoutDuration > 0 {
		ctx.JobContext = withExecutionTimeout(ctx.JobContext, timeoutDuration, label)
	}
	attempts := retryPolicyAttempts(retry)
	for attempt := 1; attempt <= attempts; attempt++ {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		var routeErr *catchRouteError
		if errors.As(err, &routeErr) {
			return err
		}
		failure, _ := failureFromError(err)
		if attempt < attempts && shouldRetryFailure(err, failure, retry) {
			if delay := retryDelay(retry, attempt); delay > 0 {
				if awaitErr := ctx.JobContext.AwaitDuration(swf.Duration(delay)); awaitErr != nil {
					return awaitErr
				}
			}
			continue
		}
		return err
	}
	return nil
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
