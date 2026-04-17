package compiler

import (
	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/swf-go/pkg/swf"
)

func artifactsToMap(artifacts []swf.Artifact) map[string]recipeartifacts.Ref {
	out := make(map[string]recipeartifacts.Ref, len(artifacts))
	for _, artifact := range artifacts {
		if artifact == nil {
			continue
		}
		artifactRef, err := recipeartifacts.RefFromArtifact(artifact)
		if err != nil {
			continue
		}
		out[artifactRef.NameValue()] = artifactRef
	}
	return out
}

func mergeArtifactRefs(maps ...map[string]recipeartifacts.Ref) map[string]recipeartifacts.Ref {
	total := 0
	for _, refs := range maps {
		total += len(refs)
	}
	if total == 0 {
		return nil
	}
	out := make(map[string]recipeartifacts.Ref, total)
	for _, refs := range maps {
		for name, artifactRef := range refs {
			out[name] = artifactRef
		}
	}
	return out
}

func lastSequenceArtifacts(resCtx *template.ResolutionContext, sequence []recipe.Node) map[string]recipeartifacts.Ref {
	if len(sequence) == 0 || resCtx == nil {
		return nil
	}
	lastNode := sequence[len(sequence)-1]
	metadata := lastNode.GetMetadata()

	var scopeType template.ScopeType
	fallback := ""
	switch t := lastNode.NodeImpl.(type) {
	case *recipe.NodeOp:
		scopeType = template.ScopeOp
		fallback = t.OpData.Op
	case *recipe.NodeSequence:
		scopeType = template.ScopeSequence
	case *recipe.NodeState:
		scopeType = template.ScopeStateMachine
	default:
		return nil
	}

	scopeID := template.ScopeID(metadata, fallback, scopeType)
	if scopeID == "" {
		return nil
	}
	step, ok := resCtx.TemplateData.Sequence[scopeID]
	if !ok {
		return nil
	}
	return step.Artifacts
}

func stateArtifacts(resCtx *template.ResolutionContext, stateName string, stateDef recipe.State) map[string]recipeartifacts.Ref {
	if resCtx == nil {
		return nil
	}
	if stateName == "" {
		return nil
	}
	metadata := recipe.NodeMetadata{}
	if stateDef.NodeImpl != nil {
		metadata = stateDef.GetMetadata()
	}
	scopeID := template.ScopeID(metadata, stateName, template.ScopeState)
	if scopeID == "" {
		return nil
	}
	step, ok := resCtx.TemplateData.States[scopeID]
	if !ok {
		return nil
	}
	return step.Artifacts
}
