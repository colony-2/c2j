package compiler

import "github.com/colony-2/c2j/pkg/recipe"

func prepareCompositeInputs(metadata recipe.NodeMetadata, raw map[string]interface{}) (map[string]interface{}, error) {
	internal := metadata.Internal
	if internal == nil || (internal.Inline == nil && internal.CompositeInputSchema == nil && internal.CompositeSchemaVersion == 0) {
		return raw, nil
	}
	contract, err := recipe.CompileInputSchema(internal.CompositeSchemaVersion, internal.CompositeInputSchema)
	if err != nil {
		return nil, err
	}
	return contract.ApplyIncluded(raw)
}
