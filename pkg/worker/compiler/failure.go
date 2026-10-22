package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type recipeFailureError struct {
	Failure *recipe.RuntimeFailure
	Err     error
}

func (e *recipeFailureError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Failure != nil {
		return e.Failure.Message
	}
	return "recipe failure"
}

func (e *recipeFailureError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type catchRouteError struct {
	Transition template.TransitionData
}

func (e *catchRouteError) Error() string {
	return fmt.Sprintf("catch routed to state %q", e.Transition.To)
}

func newRecipeFailureError(failure *recipe.RuntimeFailure, err error) error {
	if failure == nil {
		return err
	}
	return &recipeFailureError{Failure: failure.Clone(), Err: err}
}

func failureFromError(err error) (*recipe.RuntimeFailure, bool) {
	var failureErr *recipeFailureError
	if errors.As(err, &failureErr) && failureErr.Failure != nil {
		return failureErr.Failure.Clone(), true
	}
	var app *jobdb.AppError
	if errors.As(err, &app) {
		if value, ok := app.Payload.Attrs["c2j_failure"]; ok {
			raw, e := json.Marshal(value)
			if e == nil {
				var failure recipe.RuntimeFailure
				if json.Unmarshal(raw, &failure) == nil {
					return &failure, true
				}
			}
		}
	}
	return nil, false
}

func normalizeRuntimeFailure(err error, resCtx *template.ResolutionContext, metadata recipe.NodeMetadata, nodeType recipe.FailureNodeType, op string) *recipe.RuntimeFailure {
	if err == nil {
		return nil
	}
	if failure, ok := failureFromError(err); ok {
		return failure
	}

	failure := &recipe.RuntimeFailure{
		Kind:      recipe.FailureKindUnknown,
		Message:   err.Error(),
		Retryable: true,
		Node: recipe.FailureNode{
			ID:   template.ScopeID(metadata, op, scopeTypeForFailure(nodeType)),
			Path: failureNodePath(resCtx),
			Type: nodeType,
			Op:   op,
		},
	}

	var timeoutErr *jobdb.TimeoutError
	var systemErr *jobdb.SystemError
	var appErr *jobdb.AppError
	switch {
	case errors.As(err, &timeoutErr):
		failure.Kind = recipe.FailureKindTimeout
		failure.Code = timeoutErr.Payload.Code
		failure.Message = firstNonEmpty(timeoutErr.Payload.Message, err.Error())
		failure.Retryable = timeoutErr.Payload.Retryable
		failure.Timing = &recipe.FailureTiming{
			Timeout: timeoutErr.Payload.Kind,
			Scope:   timeoutErr.Payload.Scope,
			After:   timeoutErr.Payload.After.String(),
		}
	case errors.Is(err, context.DeadlineExceeded):
		failure.Kind = recipe.FailureKindTimeout
		failure.Code = "timeout"
		failure.Retryable = true
	case jobdb.IsSystemError(err):
		failure.Kind = recipe.FailureKindSystemError
		if errors.As(err, &systemErr) {
			failure.Code = systemErr.Payload.Code
			failure.Message = firstNonEmpty(systemErr.Payload.Message, err.Error())
			failure.Retryable = systemErr.Payload.Retryable
			if systemErr.Payload.Component != "" {
				failure.Attrs = map[string]interface{}{"component": systemErr.Payload.Component}
			}
		}
	case jobdb.IsAppError(err):
		failure.Kind = recipe.FailureKindTaskError
		if errors.As(err, &appErr) {
			failure.Message = firstNonEmpty(appErr.Payload.Message, err.Error())
			failure.Retryable = true
			failure.Attrs = jsonCompatibleMap(appErr.Payload.Attrs)
			if code, ok := failure.Attrs["code"].(string); ok {
				failure.Code = code
			}
		}
	case errors.Is(err, jobdb.ErrJobCancelled), errors.Is(err, context.Canceled):
		failure.Kind = recipe.FailureKindCancellation
		failure.Code = "cancelled"
		failure.Retryable = false
	default:
		failure.Retryable = true
	}
	return failure
}

func scopeTypeForFailure(nodeType recipe.FailureNodeType) template.ScopeType {
	switch nodeType {
	case recipe.FailureNodeOp:
		return template.ScopeOp
	case recipe.FailureNodeSequence:
		return template.ScopeSequence
	case recipe.FailureNodeStateMachine:
		return template.ScopeStateMachine
	case recipe.FailureNodeState:
		return template.ScopeState
	default:
		return template.ScopeOp
	}
}

func failureNodePath(resCtx *template.ResolutionContext) string {
	if resCtx == nil {
		return ""
	}
	return resCtx.TaskExecutionContext().Invocation.NodePath
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func jsonCompatibleMap(in map[string]interface{}) map[string]interface{} {
	if len(in) == 0 {
		return nil
	}
	data, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

type catchDecisionKind int

const (
	catchDecisionNone catchDecisionKind = iota
	catchDecisionRoute
	catchDecisionContinue
	catchDecisionFail
)

type catchDecision struct {
	Kind     catchDecisionKind
	ClauseID string
	To       string
	Payload  map[string]interface{}
	Outputs  map[string]interface{}
	Failure  *recipe.RuntimeFailure
	Error    error
}

func evaluateCatchClauses(clauses []recipe.CatchClause, failure *recipe.RuntimeFailure, resCtx *template.ResolutionContext, sourceState string, allowRoute bool, logger *slog.Logger) (catchDecision, error) {
	if len(clauses) == 0 || failure == nil {
		return catchDecision{Kind: catchDecisionNone}, nil
	}
	failureCtx := resCtx.WithFailure(failure)
	for idx, clause := range clauses {
		matches, err := failureCtx.EvaluateCEL(clause.When.String())
		if err != nil {
			return catchDecision{}, fmt.Errorf("catch clause %s when evaluation failed: %w", catchClauseLabel(idx, clause), err)
		}
		if !matches {
			continue
		}
		if clause.To != "" {
			if !allowRoute {
				return catchDecision{}, fmt.Errorf("catch clause %s cannot route with to outside a state machine", catchClauseLabel(idx, clause))
			}
			payload, err := failureCtx.ResolveMap(clause.Payload)
			if err != nil {
				return catchDecision{}, fmt.Errorf("catch clause %s payload render failed: %w", catchClauseLabel(idx, clause), err)
			}
			logCatchSelected(logger, clause, "route")
			return catchDecision{
				Kind:     catchDecisionRoute,
				ClauseID: clause.ID,
				To:       clause.To,
				Payload:  payload,
				Failure:  failure.Clone(),
			}, nil
		}
		if clause.Continue != nil {
			outputs, err := failureCtx.ResolveMap(clause.Continue.Outputs)
			if err != nil {
				return catchDecision{}, fmt.Errorf("catch clause %s continue.outputs render failed: %w", catchClauseLabel(idx, clause), err)
			}
			logCatchSelected(logger, clause, "continue")
			return catchDecision{
				Kind:     catchDecisionContinue,
				ClauseID: clause.ID,
				Outputs:  outputs,
				Failure:  failure.Clone(),
			}, nil
		}
		if clause.Fail != nil {
			rewritten, err := renderCatchFailure(clause.Fail, failureCtx, failure)
			if err != nil {
				return catchDecision{}, fmt.Errorf("catch clause %s fail render failed: %w", catchClauseLabel(idx, clause), err)
			}
			logCatchSelected(logger, clause, "fail")
			return catchDecision{
				Kind:     catchDecisionFail,
				ClauseID: clause.ID,
				Failure:  rewritten,
				Error:    newRecipeFailureError(rewritten, errors.New(rewritten.Message)),
			}, nil
		}
	}
	_ = sourceState
	return catchDecision{Kind: catchDecisionNone}, nil
}

func logCatchSelected(logger *slog.Logger, clause recipe.CatchClause, action string) {
	attrs := []any{"event", "recipe.catch.selected", "action", action}
	if clause.ID != "" {
		attrs = append(attrs, "id", clause.ID)
	}
	logger.Debug("Catch clause selected during evaluation", attrs...)
}

func catchClauseLabel(idx int, clause recipe.CatchClause) string {
	if clause.ID != "" {
		return fmt.Sprintf("%q", clause.ID)
	}
	return fmt.Sprintf("#%d", idx)
}

func renderCatchFailure(fail *recipe.CatchFail, resCtx *template.ResolutionContext, original *recipe.RuntimeFailure) (*recipe.RuntimeFailure, error) {
	kind := string(original.Kind)
	if fail.Kind != "" {
		rendered, err := resCtx.ResolveValueWithMode(fail.Kind, template.ModeInterpolation)
		if err != nil {
			return nil, err
		}
		kind, _ = rendered.(string)
	}
	code := original.Code
	if fail.Code != "" {
		rendered, err := resCtx.ResolveValueWithMode(fail.Code, template.ModeInterpolation)
		if err != nil {
			return nil, err
		}
		code, _ = rendered.(string)
	}
	message := original.Message
	if fail.Message != "" {
		rendered, err := resCtx.ResolveValueWithMode(fail.Message, template.ModeInterpolation)
		if err != nil {
			return nil, err
		}
		message, _ = rendered.(string)
	}
	attrs := map[string]interface{}{}
	if fail.Cause != nil {
		rendered, err := resCtx.ResolveValueWithMode(fail.Cause, template.ModeInterpolation)
		if err != nil {
			return nil, err
		}
		attrs["catch_cause"] = rendered
	}
	if len(attrs) == 0 {
		attrs = nil
	}
	return &recipe.RuntimeFailure{
		Kind:      recipe.FailureKind(kind),
		Code:      code,
		Message:   message,
		Retryable: original.Retryable,
		Node:      original.Node,
		Attrs:     attrs,
		Cause:     original.Clone(),
	}, nil
}

func canRouteToState(resCtx *template.ResolutionContext) bool {
	return containingStateName(resCtx) != ""
}

func containingStateName(resCtx *template.ResolutionContext) string {
	for cur := resCtx; cur != nil; cur = cur.Parent {
		if cur.ScopeType == template.ScopeState {
			return cur.TaskExecutionContext().Invocation.NodePath[strings.LastIndex(cur.TaskExecutionContext().Invocation.NodePath, "/")+1:]
		}
	}
	return ""
}

func recordSyntheticNodeOutput(parent *template.ResolutionContext, scopeType template.ScopeType, metadata recipe.NodeMetadata, fallback string, outputs map[string]interface{}) error {
	childInputs := map[string]interface{}(nil)
	if scopeType == template.ScopeSequence || scopeType == template.ScopeStateMachine {
		childInputs = map[string]interface{}{}
	}
	child, err := parent.NewChildContext(scopeType, metadata, fallback, childInputs)
	if err != nil {
		return err
	}
	child.AddExecutionWithArtifactData(outputs, nil, nil)
	return nil
}

func (e *recipeFailureError) NonRetryable() bool { return e.Failure != nil && !e.Failure.Retryable }

func (e *recipeFailureError) As(target any) bool {
	if e.Failure == nil {
		return false
	}
	value := jobdb.AppError{Payload: jobdb.AppErrorPayload{Message: e.Error(), Attrs: map[string]any{"c2j_failure": e.Failure}}}
	switch p := target.(type) {
	case **jobdb.AppError:
		*p = &value
		return true
	case *jobdb.AppError:
		*p = value
		return true
	}
	return false
}
