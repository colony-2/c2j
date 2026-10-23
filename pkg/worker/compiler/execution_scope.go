package compiler

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
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
type scopeFrame struct {
	resolver  *template.ResolutionContext
	lexical   *template.ResolutionContext
	forwarder *thinpackForwarder
}

// executeNodeScope delegates durable policy to JobDB. Callback state is private
// to each invocation, including callbacks which outlive cancellation. Only a
// frame identified by the committed scope outcome is published to the caller.
func executeNodeScope(ctx workflow.Context, parent *template.ResolutionContext, meta recipe.NodeMetadata, kind template.ScopeType, fallback string, body func(workflow.Context, *template.ResolutionContext, recipe.NodeMetadata) error) error {
	innerBody := body
	body = func(ctx workflow.Context, state *template.ResolutionContext, meta recipe.NodeMetadata) error {
		previous := state.GitProfile
		defer func() { state.GitProfile = previous }()
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
	original, err := parent.CloneAttempt()
	if err != nil {
		return err
	}
	entry := original
	var mu sync.Mutex
	frames := map[int]scopeFrame{}
	next := 0
	last := 0
	active := 0
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
		fresh, e := original.CloneAttempt()
		if e != nil {
			return e
		}
		var patch coretask.ContextPatch
		if len(raw) > 0 {
			if e = decodeJSONStrict(raw, &patch); e != nil {
				return fmt.Errorf("restart patch: %w", e)
			}
		}
		if e = fresh.ApplyContextPatch(patch); e != nil {
			return e
		}
		mu.Lock()
		entry = fresh
		mu.Unlock()
		return nil
	}
	newFrame := func(exec jobworkflow.ExecutionContext, base *template.ResolutionContext) (workflow.Context, *template.ResolutionContext, *thinpackForwarder, int, error) {
		mu.Lock()
		next++
		id := next
		active = id
		last = 0
		mu.Unlock()
		copy, e := base.CloneAttempt()
		if e != nil {
			return ctx, nil, nil, id, e
		}
		inner := ctx
		inner.JobContext = exec
		inner.CatchStateName = ""
		var f *thinpackForwarder
		if forwarding != nil {
			f = forwarding.fork(exec)
			inner.JobContext = f
		}
		if len(meta.Catch) > 0 {
			copy.Options.CatchBeforeRetry = true
		}
		return inner, copy, f, id, nil
	}
	save := func(id int, r, lexical *template.ResolutionContext, f *thinpackForwarder) {
		mu.Lock()
		frames[id] = scopeFrame{r, lexical, f}
		if id == active {
			last = id
		}
		mu.Unlock()
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
			mu.Lock()
			base := entry
			if frame, ok := frames[last]; ok {
				base = frame.lexical
			}
			mu.Unlock()
			return base, normalizeRuntimeFailure(e, base, meta, failureNodeType(kind), fallback)
		}
		options.Catch = append(options.Catch, jobworkflow.CatchRule{ID: id, Match: func(e error) bool {
			base, failure := failureContext(e)
			matched, evalErr := base.WithFailure(failure).EvaluateCEL(clause.When.String())
			return matched || evalErr != nil
		}, Handle: func(exec jobworkflow.ExecutionContext, e error) (jobworkflow.CatchResult, error) {
			base, failure := failureContext(e)
			mu.Lock()
			prior := frames[last].forwarder
			mu.Unlock()
			_, state, f, id, err := newFrame(exec, base)
			if f != nil && prior != nil {
				f.adopt(prior)
			}
			if err != nil {
				return jobworkflow.CatchResult{}, err
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
				out.BlockHandled = ctx.CatchStateName != ""
				if base.ScopeType == kind {
					state.AddExecutionWithArtifactData(decision.Outputs, nil, nil)
				} else if err = recordSyntheticNodeOutput(state, kind, meta, fallback, decision.Outputs); err != nil {
					return jobworkflow.CatchResult{}, err
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
			save(id, root, state, f)
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
		mu.Lock()
		base := entry
		mu.Unlock()
		inner, state, f, id, e := newFrame(exec, base)
		if e != nil {
			return nil, e
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
		e = body(inner, state, clean)
		result := scopeOutcome{Frame: id}
		var route *catchRouteError
		if errors.As(e, &route) {
			result.Route = &route.Transition
			e = nil
		}
		if e != nil {
			e = persistRecipeFailure(e, state, meta, kind, fallback)
		}
		// Failure normalization updates resolver views. Publish only after all
		// mutations finish: cancellation may let the caller adopt immediately.
		save(id, state, lexical, f)
		if e != nil {
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
		mu.Lock()
		result.Frame = last
		mu.Unlock()
	}
	mu.Lock()
	frame, ok := frames[result.Frame]
	mu.Unlock()
	if ok {
		parent.AdoptAttempt(frame.resolver)
		if forwarding != nil && frame.forwarder != nil {
			forwarding.adopt(frame.forwarder)
		}
	}
	if err != nil {
		if retrySteps && !isExecutionControlError(err) {
			// Exhausted task retries and scope timeouts must not trigger an
			// enclosing retry that discards completed preparation. Rehydrate
			// this marker after JobDB's serialized scope boundary on replay too.
			failure := normalizeRuntimeFailure(err, parent, meta, failureNodeType(kind), fallback)
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
	failure := normalizeRuntimeFailure(err, state, meta, failureNodeType(kind), op)
	return newRecipeFailureError(failure, err)
}

type machineCatchHandledError struct{}

func (*machineCatchHandledError) Error() string { return "state machine catch handled failure" }
