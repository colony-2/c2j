package recipe

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

const CurrentSchemaVersion = 2

// CompileInputSchema is the only version dispatch for recipe input contracts.
// Keep the authored schema intact: hashes and persisted v1 recipes must not be
// rewritten when they are loaded for replay. The evaluator sees only v2 types.
func CompileInputSchema(version int, schema map[string]InputSchema) (InputContract, error) {
	switch version {
	case 0, 1:
		return adaptLegacyInputSchema(schema)
	case CurrentSchemaVersion:
		for _, name := range inputKeys(schema) {
			if err := validateInputDeclaration(name, schema[name], 0); err != nil {
				return InputContract{}, err
			}
		}
		return InputContract{schema: schema}, nil
	default:
		return InputContract{}, fmt.Errorf("unsupported recipe schema_version %d (supported: 1, 2)", version)
	}
}

// Omission means v1; an explicitly supplied version must be an integer 1 or 2.
func validateSchemaVersionField(node *yaml.Node) error {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != "schema_version" {
			continue
		}
		value := node.Content[i+1]
		if value.Tag != "!!int" || (value.Value != "1" && value.Value != "2") {
			return fmt.Errorf("schema_version must be integer 1 or 2 at %d:%d", value.Line, value.Column)
		}
	}
	return nil
}
