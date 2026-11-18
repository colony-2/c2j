package grouping

import "github.com/invopop/jsonschema"

// JSONSchema describes authored values, which may be expressions. Runtime
// validation checks the resolved types, work presence and byte limits.
func (Spec) JSONSchema() *jsonschema.Schema {
	properties := jsonschema.NewProperties()
	properties.Set("key", &jsonschema.Schema{Type: "string", Description: "Literal group key, 1–1024 UTF-8 bytes; version it when receiver configuration changes"})
	properties.Set("content", &jsonschema.Schema{Description: "Non-null object contribution after template resolution"})
	properties.Set("artifacts", &jsonschema.Schema{Description: "Array of concrete c2j artifact references after template resolution"})
	properties.Set("max_items", &jsonschema.Schema{Description: "Integer 0–1000 after rendering; omitted or zero uses 50"})
	return &jsonschema.Schema{OneOf: []*jsonschema.Schema{
		{Type: "object", Properties: properties, Required: []string{"key"}, AdditionalProperties: jsonschema.FalseSchema},
		{Type: "string", Description: "Expression resolving to a grouping object"},
	}}
}
