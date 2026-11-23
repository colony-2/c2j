package recipe

import (
	"fmt"
	"maps"
	"slices"

	"github.com/invopop/jsonschema"
)

func checkOptionalInputsField(raw map[string]any, isOp bool) error {
	value, exists := raw["optional_inputs"]
	if !exists {
		return nil
	}
	op, _ := raw["op"].(string)
	if !isOp || !isSelectorOp(op) {
		return fmt.Errorf("optional_inputs is only supported on direct extension ops")
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("optional_inputs must be a mapping")
	}
	return nil
}

// CheckOptionalOpInputs also covers operations constructed directly in Go.
func CheckOptionalOpInputs(op string, meta NodeMetadata) error {
	if len(meta.OptionalInputs) == 0 {
		return nil
	}
	if !isSelectorOp(op) {
		return fmt.Errorf("optional_inputs is only supported on direct extension ops")
	}
	for _, key := range slices.Sorted(maps.Keys(meta.OptionalInputs)) {
		if _, exists := meta.Inputs[key]; exists {
			return fmt.Errorf("input %q is declared in both inputs and optional_inputs", key)
		}
	}
	return nil
}

func optionalOpInputsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		AdditionalProperties: &jsonschema.Schema{
			Type:    "string",
			Pattern: `^\$\{\{[\s\S]+\}\}$`,
		},
	}
}
