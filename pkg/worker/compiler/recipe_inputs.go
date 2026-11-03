package compiler

import "github.com/colony-2/c2j/pkg/recipe"

func prepareRecipeInputs(meta recipe.RecipeMetadata, raw map[string]interface{}, opts ExecutionOptions) (map[string]interface{}, error) {
	contract, err := recipe.CompileInputSchema(meta.SchemaVersion, meta.InputSchema)
	if err != nil {
		return nil, err
	}
	if opts.Mode == ExecutionModeValidate && raw == nil {
		return contract.ValidationDefaults()
	}
	return contract.Apply(raw)
}
