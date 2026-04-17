package compiler

import (
	"fmt"

	recipeartifacts "github.com/colony-2/c2j/pkg/core/artifacts"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/swf-go/pkg/swf"
)

func resolveArtifactBindings(resCtx *template.ResolutionContext, bindings map[string]interface{}) (map[string]recipeartifacts.Ref, error) {
	if len(bindings) == 0 {
		return nil, nil
	}
	resolved, err := resCtx.ResolveMap(bindings)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve templates op artifacts: %w", err)
	}
	out := make(map[string]recipeartifacts.Ref, len(resolved))
	for name, value := range resolved {
		if name == "" {
			return nil, fmt.Errorf("artifact binding name cannot be empty")
		}
		artifactRef, err := coerceArtifactRef(value)
		if err != nil {
			return nil, fmt.Errorf("artifact binding %q is invalid: %w", name, err)
		}
		out[name] = artifactRef
	}
	return out, nil
}

func coerceArtifactRef(value interface{}) (recipeartifacts.Ref, error) {
	switch v := value.(type) {
	case recipeartifacts.Ref:
		if err := v.Validate(); err != nil {
			return recipeartifacts.Ref{}, err
		}
		return v, nil
	case *recipeartifacts.Ref:
		if v == nil {
			return recipeartifacts.Ref{}, fmt.Errorf("artifact ref is nil")
		}
		if err := v.Validate(); err != nil {
			return recipeartifacts.Ref{}, err
		}
		return *v, nil
	case swf.ArtifactKey:
		if err := v.Validate(); err != nil {
			return recipeartifacts.Ref{}, err
		}
		return recipeartifacts.NewStoredRef(v), nil
	case *swf.ArtifactKey:
		if v == nil {
			return recipeartifacts.Ref{}, fmt.Errorf("artifact key is nil")
		}
		if err := v.Validate(); err != nil {
			return recipeartifacts.Ref{}, err
		}
		return recipeartifacts.NewStoredRef(*v), nil
	case map[string]interface{}:
		return coerceArtifactRefFromMap(v)
	default:
		return recipeartifacts.Ref{}, fmt.Errorf("expected artifact ref, got %T", value)
	}
}

func coerceArtifactRefFromMap(value map[string]interface{}) (recipeartifacts.Ref, error) {
	var artifactRef recipeartifacts.Ref
	if err := decodeJSONTaggedMap(value, &artifactRef); err == nil {
		if artifactRef.Kind != "" || artifactRef.Name != "" || artifactRef.Stored != nil || artifactRef.External != nil {
			if err := artifactRef.Validate(); err != nil {
				return recipeartifacts.Ref{}, err
			}
			return artifactRef, nil
		}
	}

	var key swf.ArtifactKey
	if err := decodeJSONTaggedMap(value, &key); err == nil {
		if key.JobId != "" || key.TaskOrdinal != 0 || key.Name != "" || key.SizeBytes != 0 {
			if err := key.Validate(); err != nil {
				return recipeartifacts.Ref{}, err
			}
			return recipeartifacts.NewStoredRef(key), nil
		}
	}

	return recipeartifacts.Ref{}, fmt.Errorf("expected artifact ref, got map[string]interface {}")
}
