package compiler

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/colony-2/c2j/git/pkg/gitstate"
	extops "github.com/colony-2/c2j/ops/pkg/extensions"
	recipeartifacts "github.com/colony-2/c2j/recipe-core/pkg/artifacts"
	"github.com/colony-2/c2j/recipe-core/pkg/contextual"
	"github.com/colony-2/c2j/recipe-core/pkg/ops"
	"github.com/colony-2/c2j/recipe-core/pkg/recipe"
	coretask "github.com/colony-2/c2j/recipe-core/pkg/task"
	"github.com/colony-2/c2j/recipe-template/pkg/template"
	workerops "github.com/colony-2/c2j/recipe-worker/pkg/ops"
	"github.com/colony-2/swf-go/pkg/swf"

	"github.com/colony-2/c2j/recipe-core/pkg/workflow"
)

// RecipeExecutor defines the surface area for executing recipes, states, and ops.
// This enables decorator implementations (e.g., analysis) without changing call sites.
type RecipeExecutor interface {
	ExecuteRecipe(ctx workflow.Context, r recipe.Recipe, rawRecipeInputs map[string]interface{}, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, opts ...ExecutionOptions) (map[string]interface{}, []swf.Artifact, error)
	ExecuteNode(ctx workflow.Context, parentResCtx *template.ResolutionContext, n *recipe.Node) error
	ExecuteStateMachine(ctx workflow.Context, parentContext *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, stateMap *recipe.StateMap, opts ...ExecutionOptions) error
	ExecuteOp(ctx workflow.Context, parentResolutionContext *template.ResolutionContext, metadata recipe.NodeMetadata, op string) error
	ExecuteSequence(ctx workflow.Context, rCtx *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, sequence []recipe.Node) error
}

type StateObserver interface {
	StateEntered(stateName string)
	StateExited(stateName string)
	TransitionEvalauted(expression string, result bool, nextStateIfExpressionTrue string)
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

	// we forward thin packs from one task to the next to maintain state.
	ctx.JobContext = newThinPackForwardingJobContext(ctx.JobContext)

	execOpts := normalizeExecutionOptions(opts)
	if execOpts.CELOptionsProvider == nil && d.celProvider != nil {
		execOpts.CELOptionsProvider = d.celProvider
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

	switch t := r.RecipeImpl.(type) {
	case *recipe.RecipeState:
		err = d.self().ExecuteStateMachine(ctx, rCtx, metadata, t.Outputs, t.StateMachineData.States, opts...)
	case *recipe.RecipeOp:
		err = d.self().ExecuteOp(ctx, rCtx, metadata, t.OpData.Op)
	case *recipe.RecipeSequence:
		err = d.self().ExecuteSequence(ctx, rCtx, metadata, t.Outputs, t.SequenceData.Sequence)
	default:
		return nil, nil, fmt.Errorf("unsupported recipe type: %T", t)
	}

	if err != nil {
		return nil, nil, err
	}
	return rCtx.GetLastExecution(), rCtx.GetLastArtifacts(), nil
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
		l.Error("failed to execute op", "op", op, "err", err)
		return err
	}

	l.Info("op executed successfully", "op", op)
	return nil
}

// executeOperation executes a single operation node
func (d DefaultRecipeExecutor) executeOp2(ctx workflow.Context, parentResolutionContext *template.ResolutionContext, metadata recipe.NodeMetadata, op string) error {

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

	var (
		registeredOp ops.RegisterableOp
		chain        []ops.TaskStep
		taskPrefix   string
		selectorOp   interface {
			ValidateInvocationInputs(map[string]interface{}) error
		}
	)
	if isSelectorOp(op) {
		resolvedSelectorOp, selectorRegisteredOp, err := loadSelectorOp(op, extops.ResolveOptions{
			BaseDir:          resCtx.TaskExecutionContext().Environment.WorktreePath,
			RepositorySource: resCtx.TaskExecutionContext().RecipeSource.Repo,
			RepositoryRef:    resCtx.TaskExecutionContext().RecipeSource.Ref,
		})
		if err != nil {
			return err
		}
		registeredOp = selectorRegisteredOp
		chain = registeredOp.TaskChain()
		taskPrefix = registeredOp.GetMetadata().Type
		selectorOp = resolvedSelectorOp
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
			normalizedInput, err := NormalizeOpInput(chain[0].InputType, selectorInvocationInput(
				op,
				resolvedNodeInputs,
				resCtx.TaskExecutionContext().RecipeSource.Repo,
				resCtx.TaskExecutionContext().RecipeSource.Ref,
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
	if metadata.Timeout > 0 {
		timeout := swf.Duration(metadata.Timeout)
		runPolicy.TotalTimeout = &timeout
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
					return err
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
	resCtx.AddExecutionWithArtifactData(stepInput, stepArtifacts, stepOutputArtifacts)
	return nil
}

func (d DefaultRecipeExecutor) innerSequence(ctx workflow.Context, parentCtx *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, sequence []recipe.Node) error {
	// Create resolution context for this sequence
	resolvedInputs, err := parentCtx.ResolveMap(metadata.Inputs)
	if err != nil {
		return fmt.Errorf("failed to resolve sequence inputs: %w", err)
	}

	resCtx, err := parentCtx.NewChildContext(template.ScopeSequence, metadata, "", resolvedInputs)
	if err != nil {
		return fmt.Errorf("failed to create resolution context: %w", err)
	}
	if err := seedSequencePlaceholders(resCtx, sequence); err != nil {
		return err
	}

	for i, node := range sequence {
		// Execute the node
		err := d.self().ExecuteNode(ctx, resCtx, &node)
		if err != nil {
			return fmt.Errorf("sequence node %d failed: %w", i, err)
		}
	}

	outputs, err := resCtx.ResolveMap(outputTemplate)
	if err != nil {
		return fmt.Errorf("failed to resolve sequence outputs: %w", err)
	}

	// add resolved output to parent context.
	parentCtx.AddExecutionWithArtifactData(outputs, lastSequenceArtifacts(resCtx, sequence), resCtx.GetLastArtifacts())
	return nil
}

func (d DefaultRecipeExecutor) ExecuteSequence(ctx workflow.Context, rCtx *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, sequence []recipe.Node) error {
	timeout := time.Duration(metadata.Timeout)
	if timeout == 0 {
		timeout = 30 * time.Second // Default timeout
	}
	fn := func(inner workflow.Context) error {
		e := d.innerSequence(inner, rCtx, metadata, outputTemplate, sequence)
		return e
	}
	err := executeCompositeInEnvelope(ctx, metadata.Retry, timeout, fn)

	return err
}

// executeCompositeInEnvelope executes a composite nodes in a retry/timeout envelope
func executeCompositeInEnvelope(ctx workflow.Context, retry *recipe.RetryPolicy, timeoutDuration time.Duration, fn func(inner workflow.Context) error) error {
	// TODO: update composite executions to respect retry policy and timeouts.
	return fn(ctx)
}
