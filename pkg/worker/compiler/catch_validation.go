package compiler

import (
	"fmt"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
)

var validationFailure = &recipe.RuntimeFailure{
	Kind:      recipe.FailureKindUnknown,
	Message:   "validation placeholder failure",
	Retryable: false,
	Node: recipe.FailureNode{
		ID:   "validation-node",
		Path: "validation/node",
		Type: recipe.FailureNodeState,
	},
}

func validateCatchSemantics(r recipe.Recipe, rootCtx *template.ResolutionContext) error {
	switch t := r.RecipeImpl.(type) {
	case *recipe.RecipeOp:
		return validateCatchClauses(t.NodeMetadata.Catch, rootCtx, nil)
	case *recipe.RecipeSequence:
		if err := validateCatchClauses(t.NodeMetadata.Catch, rootCtx, nil); err != nil {
			return err
		}
		return validateSequenceCatchSemantics(rootCtx, t.NodeMetadata, t.Sequence, nil, true)
	case *recipe.RecipeState:
		stateNames := stateNameSet(t.StateMachineData.States)
		if err := validateCatchClauses(t.NodeMetadata.Catch, rootCtx, stateNames); err != nil {
			return err
		}
		smCtx, err := validationStateMachineContext(rootCtx, t.NodeMetadata, t.StateMachineData.States)
		if err != nil {
			return err
		}
		return validateStateMapCatchSemantics(smCtx, t.StateMachineData.States)
	default:
		return nil
	}
}

func validateStateMapCatchSemantics(smCtx *template.ResolutionContext, stateMap *recipe.StateMap) error {
	if stateMap == nil {
		return nil
	}
	stateNames := stateNameSet(stateMap)
	for name, state := range stateMap.States {
		stateCtx, err := smCtx.NewChildContext(template.ScopeState, state.GetMetadata(), name, nil)
		if err != nil {
			return err
		}
		stateCtx.TemplateData.Transition = template.NewFailureTransitionData("", name, nil, validationFailure)
		if err := stateCtx.ResolveVars(state.GetMetadata().Vars); err != nil {
			return err
		}
		if err := validateCatchClauses(state.GetMetadata().Catch, stateCtx, stateNames); err != nil {
			return err
		}
		stateNode := nodeWithoutVars(state.Node)
		if err := validateNodeCatchSemantics(stateCtx, stateNode, stateNames); err != nil {
			return err
		}
	}
	return nil
}

func validateNodeCatchSemantics(parent *template.ResolutionContext, node recipe.Node, stateNames map[string]struct{}) error {
	switch n := node.NodeImpl.(type) {
	case *recipe.NodeOp:
		return validateCatchClauses(n.NodeMetadata.Catch, parent, stateNames)
	case *recipe.NodeSequence:
		if err := validateCatchClauses(n.NodeMetadata.Catch, parent, stateNames); err != nil {
			return err
		}
		return validateSequenceCatchSemantics(parent, n.NodeMetadata, n.Sequence, stateNames, true)
	case *recipe.NodeState:
		nestedNames := stateNameSet(n.StateMachineData.States)
		if err := validateCatchClauses(n.NodeMetadata.Catch, parent, nestedNames); err != nil {
			return err
		}
		smCtx, err := validationStateMachineContext(parent, n.NodeMetadata, n.StateMachineData.States)
		if err != nil {
			return err
		}
		return validateStateMapCatchSemantics(smCtx, n.StateMachineData.States)
	default:
		return nil
	}
}

func validateSequenceCatchSemantics(parent *template.ResolutionContext, metadata recipe.NodeMetadata, sequence []recipe.Node, stateNames map[string]struct{}, skipSelf bool) error {
	resolvedInputs, err := parent.ResolveMap(metadata.Inputs)
	if err != nil {
		return err
	}
	seqCtx, err := parent.NewChildContext(template.ScopeSequence, metadata, "", resolvedInputs)
	if err != nil {
		return err
	}
	if err := seqCtx.ResolveVars(metadata.Vars); err != nil {
		return err
	}
	if err := seedSequencePlaceholders(seqCtx, sequence); err != nil {
		return err
	}
	if !skipSelf {
		if err := validateCatchClauses(metadata.Catch, seqCtx, stateNames); err != nil {
			return err
		}
	}
	for _, child := range sequence {
		if err := validateNodeCatchSemantics(seqCtx, child, stateNames); err != nil {
			return err
		}
	}
	return nil
}

func validationStateMachineContext(parent *template.ResolutionContext, metadata recipe.NodeMetadata, stateMap *recipe.StateMap) (*template.ResolutionContext, error) {
	resolvedInputs, err := parent.ResolveMap(metadata.Inputs)
	if err != nil {
		return nil, err
	}
	smCtx, err := parent.NewChildContext(template.ScopeStateMachine, metadata, "", resolvedInputs)
	if err != nil {
		return nil, err
	}
	if err := smCtx.ResolveVars(metadata.Vars); err != nil {
		return nil, err
	}
	if stateMap != nil {
		if err := seedStateMachinePlaceholders(smCtx, stateMap); err != nil {
			return nil, err
		}
	}
	return smCtx, nil
}

func validateCatchClauses(clauses []recipe.CatchClause, resCtx *template.ResolutionContext, stateNames map[string]struct{}) error {
	failureCtx := resCtx.WithFailure(validationFailure)
	for i, clause := range clauses {
		if err := clause.Validate(); err != nil {
			return fmt.Errorf("catch_invalid_shape: catch clause %s: %w", catchClauseLabel(i, clause), err)
		}
		if _, err := failureCtx.EvaluateCEL(clause.When.String()); err != nil {
			return fmt.Errorf("catch_when_invalid: catch clause %s: %w", catchClauseLabel(i, clause), err)
		}
		if clause.To != "" {
			if stateNames == nil {
				return fmt.Errorf("catch_to_without_state_machine: catch clause %s routes to %q outside a state machine", catchClauseLabel(i, clause), clause.To)
			}
			if _, ok := stateNames[clause.To]; !ok {
				return fmt.Errorf("catch_to_unknown_state: catch clause %s routes to unknown state %q", catchClauseLabel(i, clause), clause.To)
			}
			if _, err := failureCtx.ResolveMap(clause.Payload); err != nil {
				return fmt.Errorf("catch_payload_invalid: catch clause %s: %w", catchClauseLabel(i, clause), err)
			}
		}
		if clause.Continue != nil {
			if _, err := failureCtx.ResolveMap(clause.Continue.Outputs); err != nil {
				return fmt.Errorf("catch_continue_outputs_invalid: catch clause %s: %w", catchClauseLabel(i, clause), err)
			}
		}
		if clause.Fail != nil {
			if clause.Fail.Kind != "" {
				rendered, err := failureCtx.ResolveValueWithMode(clause.Fail.Kind, template.ModeInterpolation)
				if err != nil {
					return fmt.Errorf("catch_fail_kind_invalid: catch clause %s: %w", catchClauseLabel(i, clause), err)
				}
				kind, _ := rendered.(string)
				if !validFailureKind(kind) {
					return fmt.Errorf("catch_fail_kind_invalid: catch clause %s uses unknown failure kind %q", catchClauseLabel(i, clause), kind)
				}
			}
			if _, err := renderCatchFailure(clause.Fail, failureCtx, validationFailure); err != nil {
				return fmt.Errorf("catch_fail_invalid: catch clause %s: %w", catchClauseLabel(i, clause), err)
			}
		}
	}
	return nil
}

func validFailureKind(kind string) bool {
	switch recipe.FailureKind(kind) {
	case recipe.FailureKindTimeout, recipe.FailureKindTaskError, recipe.FailureKindSystemError, recipe.FailureKindCancellation, recipe.FailureKindUnknown:
		return true
	default:
		return false
	}
}

func stateNameSet(stateMap *recipe.StateMap) map[string]struct{} {
	if stateMap == nil {
		return nil
	}
	out := make(map[string]struct{}, len(stateMap.States))
	for name := range stateMap.States {
		out[name] = struct{}{}
	}
	return out
}
