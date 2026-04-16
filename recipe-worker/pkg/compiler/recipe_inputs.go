package compiler

import (
	"fmt"

	"github.com/colony-2/c2j/recipe-core/pkg/recipe"
)

func prepareRecipeInputs(meta recipe.RecipeMetadata, raw map[string]interface{}, opts ExecutionOptions) (map[string]interface{}, error) {
	if opts.Mode == ExecutionModeValidate && raw == nil {
		return validationDefaultsFromSchema(meta.InputSchema)
	}
	return meta.ValidateInputShapeAndFillDefaults(raw)
}

func validationDefaultsFromSchema(schema map[string]recipe.InputSchema) (map[string]interface{}, error) {
	inputs := make(map[string]interface{}, len(schema))
	for key, def := range schema {
		if def.Default != nil {
			inputs[key] = def.Default
			continue
		}

		switch def.Type {
		case "string":
			inputs[key] = ""
		case "number":
			inputs[key] = 0
		case "boolean":
			inputs[key] = false
		default:
			return nil, fmt.Errorf("unsupported input type for %s: %s", key, def.Type)
		}
	}
	return inputs, nil
}
