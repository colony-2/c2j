package compiler

import (
	"fmt"

	"github.com/colony-2/c2j/pkg/core/recipe"
	"github.com/colony-2/c2j/pkg/template"
)

func seedSequencePlaceholders(resCtx *template.ResolutionContext, sequence []recipe.Node) error {
	if !resCtx.Options.AllowFutureStepRefs {
		return nil
	}

	for _, node := range sequence {
		key, outputs, err := nodePlaceholder(node)
		if err != nil {
			return err
		}
		if key == "" {
			continue
		}
		if _, exists := resCtx.TemplateData.Sequence[key]; exists {
			continue
		}
		resCtx.TemplateData.Sequence[key] = placeholderStepOutput(outputs)
	}
	return nil
}

func seedStateMachinePlaceholders(resCtx *template.ResolutionContext, stateMap *recipe.StateMap) error {
	if !resCtx.Options.AllowFutureStepRefs {
		return nil
	}
	if stateMap == nil {
		return nil
	}
	for stateName, state := range stateMap.States {
		key := template.ScopeID(state.Node.GetMetadata(), stateName, template.ScopeState)
		if _, exists := resCtx.TemplateData.States[key]; exists {
			continue
		}
		outputs, err := nodeOutputPlaceholder(state.Node)
		if err != nil {
			return err
		}
		resCtx.TemplateData.States[key] = placeholderStepOutput(outputs)
	}
	return nil
}

func nodePlaceholder(node recipe.Node) (string, map[string]interface{}, error) {
	metadata := node.GetMetadata()
	switch t := node.NodeImpl.(type) {
	case *recipe.NodeOp:
		outputs, err := zeroOutputForOp(t.OpData.Op)
		if err != nil {
			return "", nil, err
		}
		key := template.ScopeID(metadata, t.OpData.Op, template.ScopeOp)
		return key, outputs, nil
	case *recipe.NodeSequence:
		outputs := zeroOutputsFromTemplateMap(t.Outputs)
		key := template.ScopeID(metadata, "", template.ScopeSequence)
		return key, outputs, nil
	case *recipe.NodeState:
		outputs := zeroOutputsFromTemplateMap(t.Outputs)
		key := template.ScopeID(metadata, "", template.ScopeStateMachine)
		return key, outputs, nil
	case *recipe.NodeShared:
		return "", nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported node type: %T", t)
	}
}

func nodeOutputPlaceholder(node recipe.Node) (map[string]interface{}, error) {
	switch t := node.NodeImpl.(type) {
	case *recipe.NodeOp:
		return zeroOutputForOp(t.OpData.Op)
	case *recipe.NodeSequence:
		return zeroOutputsFromTemplateMap(t.Outputs), nil
	case *recipe.NodeState:
		return zeroOutputsFromTemplateMap(t.Outputs), nil
	case *recipe.NodeShared:
		return map[string]interface{}{}, nil
	default:
		return nil, fmt.Errorf("unsupported node type: %T", t)
	}
}
