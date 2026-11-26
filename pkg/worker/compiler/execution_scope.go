package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type scopeOutcome struct {
	BlockHandled bool                     `json:"block_handled,omitempty"`
	Frame        int                      `json:"frame"`
	Route        *template.TransitionData `json:"route,omitempty"`
}
type scopeCompletion struct {
	scope  *template.ResolutionContext
	output template.ExecutionOutput
	status workflow.CompletionStatus
}

// completeNodeScope hands the result to the executor. Callers without a durable
// scope, including validation, publish directly into their own context.
func completeNodeScope(ctx workflow.Context, scope *template.ResolutionContext, output template.ExecutionOutput) error {
	if declared, ok := output.Outputs["artifact_refs"].(map[string]any); ok {
		refs, err := bindResolvedArtifacts(declared)
		if err != nil {
			return fmt.Errorf("result artifact_refs: %w", err)
		}
		// Binding no artifacts returns nil, but an explicitly declared output
		// object must remain {} for callers and their output contracts.
		if refs == nil && declared != nil {
			refs = map[string]recipeartifacts.Ref{}
		}
		output.Outputs["artifact_refs"] = refs
		output.ArtifactRefs = mergeArtifactRefs(output.ArtifactRefs, refs)
	}
	if ctx.ScopeOutput != nil {
		return ctx.ScopeOutput(scope, output)
	}
	return scope.PublishExecution(output)
}

type scopeFrame struct {
	completion *scopeCompletion
	resolver   *template.ResolutionContext
	lexical    *template.ResolutionContext
	forwarder  *thinpackForwarder
	failure    error
}

// JobDB persists native timeouts ahead of application attributes. Scope bodies
// are reconstructed on replay, so recover the originating recipe failure from
// this attempt's callback when its native timeout matches the accepted outcome.
// A timeout of the enclosing scope itself must not inherit a child's evidence.
func restoreScopeTimeoutFailure(err, bodyErr error) error {
	if _, ok := failureFromError(err); ok {
		return err
	}
	failure, ok := failureFromError(bodyErr)
	if !ok {
		return err
	}
	var accepted, body *jobdb.TimeoutError
	if errors.As(err, &accepted) && errors.As(bodyErr, &body) && reflect.DeepEqual(accepted.Payload, body.Payload) {
		return newRecipeFailureError(failure, err)
	}
	return err
}

// executeNodeScope delegates durable policy to JobDB. Scope bodies, handlers,
// and input builders run serially. They share context data; only the accepted
// scope outcome publishes its prepared completion to the enclosing context.
func executeNodeScope(ctx workflow.Context, parent *template.ResolutionContext, meta recipe.NodeMetadata, kind template.ScopeType, fallback string, body func(workflow.Context, *template.ResolutionContext, recipe.NodeMetadata) error) error {
	return executeNodeScopeWithCatchFilter(ctx, parent, meta, kind, fallback, body, nil)
}

func executeNodeScopeWithCatchFilter(ctx workflow.Context, parent *template.ResolutionContext, meta recipe.NodeMetadata, kind template.ScopeType, fallback string, body func(workflow.Context, *template.ResolutionContext, recipe.NodeMetadata) error, catchFilter func(error) bool) error {
	innerBody := body
	body = func(ctx workflow.Context, state *template.ResolutionContext, meta recipe.NodeMetadata) error {
		previous := state.GitProfile
		previousPerms := state.Permissions
		defer func() { state.GitProfile = previous; state.Permissions = previousPerms }()
		var err error
		// Include inputs belong to the caller. Enter the callee's authority only
		// after resolving them, when NewChildContext creates the included scope.
		if meta.Internal == nil || (meta.Internal.Inline == nil && meta.Internal.Late == nil) {
			state.Permissions, err = state.Permissions.Enter(meta.Perms, false)
		}
		if err != nil {
			return err
		}
		op := ""
		if kind == template.ScopeOp {
			op = fallback
		}
		if err := state.SelectGitProfile(meta.GitProfile, op); err != nil {
			return err
		}
		return innerBody(ctx, state, meta)
	}
	if parent.Options.Mode == template.ModeValidate {
		return body(ctx, parent, meta)
	}
	var metricView *metrics.View
	if ctx.DeferScopeOutput {
		// A late include's preparation and body publish one logical node.
		// Keep its preparation reports in that node's eventual snapshot.
		metricView = parent.Options.MetricsScope
	} else if m := taskMetrics(ctx.JobContext.Context()); m != nil {
		var close func()
		metricView, close = m.EnterScope()
		defer close()
	}
	original := parent
	entry := original
	frames := map[int]scopeFrame{}
	next := 0
	last := 0
	var contexts map[int]workflow.Context
	if ctx.OnObservation != nil {
		contexts = map[int]workflow.Context{}
	}
	var scopeDeadline time.Time
	var forwarding *thinpackForwarder
	if f, ok := ctx.JobContext.(*thinpackForwarder); ok {
		forwarding = f
	}
	options := jobworkflow.ScopeOptions{TotalTimeout: time.Duration(meta.Timeout)}
	retrySteps := false
	if kind == template.ScopeOp {
		if op, ok := ops.Get(fallback); ok {
			retrySteps = op.GetMetadata().RetrySteps
		}
	}
	if !retrySteps && meta.Retry != nil && (!parent.Options.CatchBeforeRetry || len(meta.Catch) > 0) {
		options.Retry = *meta.Retry
	}
	options.OnRestart = func(raw json.RawMessage) error {
		fresh, e := original.CloneForRestart()
		if e != nil {
			return e
		}
		if e = fresh.ApplyRestartPayload(raw); e != nil {
			return e
		}
		entry = fresh
		return nil
	}
	newFrame := func(exec jobworkflow.ExecutionContext, base *template.ResolutionContext) (workflow.Context, *template.ResolutionContext, *thinpackForwarder, *scopeCompletion, int, error) {
		next++
		id := next
		last = 0
		view, err := base.WithEvaluationContext(exec.Context())
		if err != nil {
			return workflow.Context{}, nil, nil, nil, 0, err
		}
		view.Options.MetricsScope = metricView
		inner := ctx
		inner.DeferScopeOutput = false
		inner.InlineRootStatus = nil
		inner.JobContext = exec
		inner.CatchStateName = ""
		completion := &scopeCompletion{}
		completion.status.Declaration = meta.Status
		inner.ScopeStatus = func(status workflow.CompletionStatus) { completion.status = status }
		inner.ScopeOutput = func(scope *template.ResolutionContext, output template.ExecutionOutput) error {
			if err := exec.Context().Err(); err != nil {
				return err
			}
			if completion.scope != nil {
				return template.ErrAlreadyWritten
			}
			completion.scope = scope
			completion.output = output
			if ctx.RecordStatus != nil && completion.status.Declaration != nil && completion.status.Resolver == nil {
				completion.status.Resolver = scope.ExecutionView()
			}
			return nil
		}
		var f *thinpackForwarder
		if forwarding != nil {
			f = forwarding.fork(exec)
			inner.JobContext = f
		}
		if len(meta.Catch) > 0 {
			view.Options.CatchBeforeRetry = true
		}
		if contexts != nil {
			contexts[id] = inner
		}
		return inner, view, f, completion, id, nil
	}
	save := func(id int, r, lexical *template.ResolutionContext, f *thinpackForwarder, completion *scopeCompletion) {
		frames[id] = scopeFrame{resolver: r, lexical: lexical, forwarder: f, completion: completion}
		last = id
	}
	clauses := meta.Catch
	if kind == template.ScopeStateMachine && ctx.CatchStateName == "" {
		clauses = nil
	}
	for index, clause := range clauses {
		id := clause.ID
		if id == "" {
			id = fmt.Sprintf("catch-%d", index)
		}
		failureContext := func(e error) (*template.ResolutionContext, *recipe.RuntimeFailure) {
			base := entry
			if frame, ok := frames[last]; ok {
				base = frame.lexical
				e = restoreScopeTimeoutFailure(e, frame.failure)
			}
			return base, normalizeRuntimeFailure(e, base, meta, failureNodeType(kind), fallback)
		}
		options.Catch = append(options.Catch, jobworkflow.CatchRule{ID: id, Match: func(e error) bool {
			if catchFilter != nil && !catchFilter(e) {
				return false
			}
			base, failure := failureContext(e)
			// A failed child/attempt may already be canceled. Match in the
			// enclosing lifetime, bounded by this scope's continuous deadline.
			matchCtx := ctx.JobContext.Context()
			if !scopeDeadline.IsZero() {
				var cancel context.CancelFunc
				matchCtx, cancel = context.WithDeadline(matchCtx, scopeDeadline)
				defer cancel()
			}
			view, evalErr := base.WithEvaluationContext(matchCtx)
			if evalErr != nil {
				return true // The handler reports the expired execution context.
			}
			matched, evalErr := view.WithFailure(failure).EvaluateCEL(clause.When.String())
			return matched || evalErr != nil
		}, Handle: func(exec jobworkflow.ExecutionContext, e error) (jobworkflow.CatchResult, error) {
			base, failure := failureContext(e)
			prior := frames[last].forwarder
			inner, state, f, completion, id, frameErr := newFrame(exec, base)
			if frameErr != nil {
				return jobworkflow.CatchResult{}, frameErr
			}
			if f != nil && prior != nil {
				f.adopt(prior)
			}
			sourceState := containingStateName(state)
			if ctx.CatchStateName != "" {
				sourceState = ctx.CatchStateName
			}
			decision, err := evaluateCatchClauses([]recipe.CatchClause{clause}, failure, state, sourceState, sourceState != "", ctx.GetLogger())
			if err != nil {
				return jobworkflow.CatchResult{}, err
			}
			out := scopeOutcome{Frame: id}
			switch decision.Kind {
			case catchDecisionContinue:
				if err := validateExpectedOutputs(meta.ExpectOutputs, decision.Outputs); err != nil {
					return jobworkflow.CatchResult{}, err
				}
				out.BlockHandled = ctx.CatchStateName != ""
				if base.ScopeType == kind && (meta.Internal == nil || meta.Internal.Late == nil || ctx.CatchStateName != "") {
					if err = completeNodeScope(inner, state, template.ExecutionOutput{Outputs: decision.Outputs}); err != nil {
						return jobworkflow.CatchResult{}, err
					}
				} else if err = recordSyntheticNodeOutput(inner, state, kind, meta, fallback, decision.Outputs); err != nil {
					return jobworkflow.CatchResult{}, err
				}
				if completion.status.Resolver != nil {
					completion.status.Resolver = completion.status.Resolver.WithFailure(failure)
				}
			case catchDecisionRoute:
				route := template.NewFailureTransitionData(sourceState, decision.To, decision.Payload, decision.Failure)
				out.Route = &route
			case catchDecisionFail:
				return jobworkflow.CatchResult{Disposition: jobworkflow.CatchContinue, Err: persistRecipeFailure(decision.Error, state, meta, kind, fallback)}, nil
			default:
				return jobworkflow.CatchResult{}, fmt.Errorf("selected catch %q did not match", clause.ID)
			}
			root := state
			for root.Parent != nil && root.TaskExecutionContext().Invocation.NodePath != parent.TaskExecutionContext().Invocation.NodePath {
				root = root.Parent
			}
			save(id, root, state, f, completion)
			data, err := jobdb.NewTaskData(out)
			return jobworkflow.CatchResult{Disposition: jobworkflow.CatchHandled, Output: data}, err
		}})
	}
	site := string(kind) + ":" + template.ScopeID(meta, fallback, kind)
	if ctx.BeforeScope != nil {
		if err := ctx.BeforeScope(ctx.JobContext.Context(), site, original); err != nil {
			return &executionControlError{err}
		}
	}
	out, err := ctx.JobContext.DoScope(site, options, func(exec jobworkflow.ExecutionContext) (jobdb.JobData, error) {
		base := entry
		scopeDeadline, _ = exec.Context().Deadline()
		inner, state, f, completion, id, frameErr := newFrame(exec, base)
		if frameErr != nil {
			return nil, frameErr
		}
		clean := meta
		clean.Catch = nil
		if kind == template.ScopeStateMachine && ctx.CatchStateName == "" {
			clean.Catch = meta.Catch
		}
		if !retrySteps {
			clean.Retry = nil
		}
		clean.Timeout = 0
		lexical := state
		inner.ScopeState = func(value *template.ResolutionContext) { lexical = value }
		e := body(inner, state, clean)
		result := scopeOutcome{Frame: id}
		var route *catchRouteError
		if errors.As(e, &route) {
			result.Route = &route.Transition
			e = nil
		}
		// Transparent late-entry scopes must carry a handled machine outcome
		// as control flow, just like a catch route, across durable replay.
		var handled *machineCatchHandledError
		if errors.As(e, &handled) {
			result.BlockHandled = true
			e = nil
		}
		if e != nil {
			e = persistRecipeFailure(e, state, meta, kind, fallback)
		}
		// Save the completed callback before JobDB selects its outcome.
		save(id, state, lexical, f, completion)
		if e != nil {
			frame := frames[id]
			frame.failure = e
			frames[id] = frame
			return nil, e
		}
		return jobdb.NewTaskData(result)
	})
	var result scopeOutcome
	if err == nil {
		raw, e := out.GetData()
		if e != nil {
			return e
		}
		if e = json.Unmarshal(raw, &result); e != nil {
			return e
		}
	} else {
		result.Frame = last
	}
	frame, ok := frames[result.Frame]
	// A child node within a state returns its output to that state. Only the
	// accepted state outcome publishes to the machine's states map.
	deferToState := parent.ScopeType == template.ScopeState && kind != template.ScopeState && ctx.ScopeOutput != nil
	deferToOuter := ctx.DeferScopeOutput && ctx.ScopeOutput != nil
	if ok {
		if err == nil && frame.completion.scope != nil && !deferToState && !deferToOuter {
			if e := frame.completion.scope.PublishExecution(frame.completion.output); e != nil {
				return e
			}
			if ctx.InlineRootStatus != nil {
				ctx.InlineRootStatus(frame.completion.status)
			} else if ctx.RecordStatus != nil {
				if e := ctx.RecordStatus(ctx.JobContext, site, frame.completion.status); e != nil {
					return e
				}
			}
		}
		if entry != original {
			parent.AdoptRestart(frame.resolver)
		} else {
			parent.AdoptExecution(frame.resolver)
		}
		if forwarding != nil && frame.forwarder != nil {
			forwarding.adopt(frame.forwarder)
		}
	}
	if err != nil {
		if ok {
			err = restoreScopeTimeoutFailure(err, frame.failure)
		}
		if retrySteps && !isExecutionControlError(err) {
			// Exhausted task retries and scope timeouts must not trigger an
			// enclosing retry that discards completed preparation. Rehydrate
			// this marker after JobDB's serialized scope boundary on replay too.
			failure := normalizeScopeFailure(err, parent, meta, kind, fallback)
			failure.Retryable = false
			// Keep the normalized failure details, but do not unwrap the spent
			// task error: JobDB prioritizes a TimeoutError's retryable flag over
			// the outer NonRetryable marker.
			return newRecipeFailureError(failure, nil)
		}
		return persistRecipeFailure(err, parent, meta, kind, fallback)
	}
	if !ok {
		return fmt.Errorf("scope %s outcome has no reconstructed frame %d", site, result.Frame)
	}
	if frame.completion.scope != nil && (deferToOuter || deferToState) && ctx.ScopeStatus != nil {
		ctx.ScopeStatus(frame.completion.status)
	}
	if deferToOuter && frame.completion.scope != nil {
		if e := completeNodeScope(ctx, frame.completion.scope, frame.completion.output); e != nil {
			return e
		}
	} else if deferToState && frame.completion.scope != nil {
		if e := completeNodeScope(ctx, parent, frame.completion.output); e != nil {
			return e
		}
	}
	if frame.completion.scope != nil && ctx.OnObservation != nil {
		contexts[result.Frame].Observe(workflow.Observation{Kind: "scope_completed", Scope: frame.completion.scope, Output: frame.completion.output})
	}
	if result.BlockHandled {
		return &machineCatchHandledError{}
	}
	if result.Route != nil {
		return &catchRouteError{Transition: *result.Route}
	}
	return nil
}
func failureNodeType(kind template.ScopeType) recipe.FailureNodeType {
	switch kind {
	case template.ScopeSequence:
		return recipe.FailureNodeSequence
	case template.ScopeStateMachine:
		return recipe.FailureNodeStateMachine
	case template.ScopeState:
		return recipe.FailureNodeState
	default:
		return recipe.FailureNodeOp
	}
}
func persistRecipeFailure(err error, state *template.ResolutionContext, meta recipe.NodeMetadata, kind template.ScopeType, op string) error {
	if isExecutionControlError(err) {
		return err
	}
	failure := normalizeScopeFailure(err, state, meta, kind, op)
	return newRecipeFailureError(failure, err)
}

// Scope callbacks receive the parent resolver. Failures raised before a task
// starts (or by the scope itself) still belong to this node, not its parent.
func normalizeScopeFailure(err error, parent *template.ResolutionContext, meta recipe.NodeMetadata, kind template.ScopeType, op string) *recipe.RuntimeFailure {
	if failure, ok := failureFromError(err); ok {
		return failure
	}
	failure := normalizeRuntimeFailure(err, parent, meta, failureNodeType(kind), op)
	if failure != nil {
		path := failureNodePath(parent)
		if path != "" {
			path += "/"
		}
		failure.Node.Path = path + template.ScopeID(meta, op, kind)
	}
	return failure
}

type machineCatchHandledError struct{}

func (*machineCatchHandledError) Error() string { return "state machine catch handled failure" }
