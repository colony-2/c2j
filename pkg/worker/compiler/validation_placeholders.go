package compiler

import (
	"fmt"

	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
)

func seedSequencePlaceholders(resCtx *template.ResolutionContext, sequence []recipe.Node) error {
	if !resCtx.Options.AllowFutureStepRefs {
		return nil
	}

	for _, node := range sequence {
		key, outputs, err := nodePlaceholder(resCtx, node)
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
		outputs, err := nodeOutputPlaceholder(resCtx, state.Node)
		if err != nil {
			return err
		}
		resCtx.TemplateData.States[key] = placeholderStepOutput(outputs)
	}
	return nil
}

func nodePlaceholder(resCtx *template.ResolutionContext, node recipe.Node) (string, map[string]interface{}, error) {
	metadata := node.GetMetadata()
	switch t := node.NodeImpl.(type) {
	case *recipe.NodeOp:
		outputs, err := zeroOutputForOp(resolvedSelector(t.OpData.Op, resCtx.Options.ResolvedSelectors), selectorLoadResolveOptionsForResolution(resCtx))
		if err != nil {
			return "", nil, err
		}
		key := template.ScopeID(metadata, t.OpData.Op, template.ScopeOp)
		return key, outputs, nil
	case *recipe.NodeInclude:
		return template.ScopeID(metadata, "", template.ScopeSequence), hookOutputPlaceholder(t), nil
	case *recipe.NodeSequence:
		outputs := zeroOutputsFromTemplateMap(t.Outputs)
		key := template.ScopeID(metadata, "", template.ScopeSequence)
		return key, outputs, nil
	case *recipe.NodeState:
		outputs := zeroOutputsFromTemplateMap(t.Outputs)
		key := template.ScopeID(metadata, "", template.ScopeStateMachine)
		return key, outputs, nil
	case *recipe.NodeChildGroup:
		key := template.ScopeID(metadata, "child_group", template.ScopeOp)
		return key, zeroOutputForChildGroup(), nil
	case *recipe.NodeShared:
		return "", nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported node type: %T", t)
	}
}

func nodeOutputPlaceholder(resCtx *template.ResolutionContext, node recipe.Node) (map[string]interface{}, error) {
	switch t := node.NodeImpl.(type) {
	case *recipe.NodeOp:
		return zeroOutputForOp(resolvedSelector(t.OpData.Op, resCtx.Options.ResolvedSelectors), selectorLoadResolveOptionsForResolution(resCtx))
	case *recipe.NodeInclude:
		return hookOutputPlaceholder(t), nil
	case *recipe.NodeSequence:
		return zeroOutputsFromTemplateMap(t.Outputs), nil
	case *recipe.NodeState:
		return zeroOutputsFromTemplateMap(t.Outputs), nil
	case *recipe.NodeChildGroup:
		return zeroOutputForChildGroup(), nil
	case *recipe.NodeShared:
		return map[string]interface{}{}, nil
	default:
		return nil, fmt.Errorf("unsupported node type: %T", t)
	}
}

func selectorLoadResolveOptionsForResolution(resCtx *template.ResolutionContext) extops.ResolveOptions {
	if resCtx == nil {
		return extops.ResolveOptions{}
	}
	if resCtx.Options.ResolvedGitRefs == nil {
		resCtx.Options.ResolvedGitRefs = map[string]string{}
	}
	resolveOpts := selectorLoadResolveOptions(resCtx.TaskExecutionContext().JobContext(), resCtx.GetGitCommitContext())
	resolveOpts.ResolvedRefs = resCtx.Options.ResolvedGitRefs
	return resolveOpts
}

// Static validation checks the callsite using its declared output shape. Actual
// hook source and policy are deliberately unavailable until live node entry.
func hookOutputPlaceholder(n *recipe.NodeInclude) map[string]any {
	var zero func(map[string]any) any
	zero = func(s map[string]any) any {
		if v, ok := s["const"]; ok {
			return v
		}
		if v, ok := s["enum"].([]any); ok && len(v) > 0 {
			return v[0]
		}
		switch s["type"] {
		case "string":
			return ""
		case "boolean":
			return false
		case "number", "integer":
			return 0
		case "array":
			return []any{}
		}
		out := map[string]any{}
		if props, ok := s["properties"].(map[string]any); ok {
			for k, v := range props {
				if schema, ok := v.(map[string]any); ok {
					out[k] = zero(schema)
				}
			}
		}
		return out
	}
	out, _ := zero(n.ExpectOutputs).(map[string]any)
	if n.Include.IfMissing != nil && len(out) == 0 {
		return zeroOutputsFromTemplateMap(n.Include.IfMissing.Outputs)
	}
	return out
}
