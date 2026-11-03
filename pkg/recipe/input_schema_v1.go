package recipe

import "fmt"

// This adapter is the complete legacy input-schema compatibility boundary.
// Remove it and the v1 dispatch in schema_version.go when legacy authoring AND
// replay are retired. Never migrate stored recipes or checkpoint wire values.
func adaptLegacyInputSchema(schema map[string]InputSchema) (InputContract, error) {
	normalized := make(map[string]InputSchema, len(schema))
	for _, name := range inputKeys(schema) {
		def := schema[name]
		if err := def.ValidateSecretDeclaration(); err != nil {
			return InputContract{}, fmt.Errorf("input %q: %w", name, err)
		}
		// These fields were unknown to the v1 decoder and had no semantics.
		def.Properties, def.Items, def.AdditionalProperties = nil, nil, nil
		switch def.Type {
		case "object":
			def.Type = "checkpoint"
		case "string", "number", "boolean", "artifact", "artifact_map":
		default:
			// V1 only rejected unsupported types when a value was supplied.
			// Preserve optional, unused declarations in old histories.
			def.sourceType, def.Type = def.Type, ""
		}
		normalized[name] = def
	}
	return InputContract{schema: normalized, allowUnspecifiedIncludeInputs: len(schema) == 0}, nil
}
