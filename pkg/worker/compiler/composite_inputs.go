package compiler

import "github.com/colony-2/c2j/pkg/recipe"

func prepareCompositeInputs(metadata recipe.NodeMetadata, raw map[string]interface{}) (map[string]interface{}, error) {
	if metadata.Internal == nil || len(metadata.Internal.CompositeInputSchema) == 0 {
		return raw, nil
	}
	if raw == nil {
		raw = map[string]interface{}{}
	}
	meta := recipe.RecipeMetadata{InputSchema: metadata.Internal.CompositeInputSchema}
	return meta.ValidateInputShapeAndFillDefaults(raw)
}
