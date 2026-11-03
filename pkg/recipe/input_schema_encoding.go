package recipe

import (
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
	"gopkg.in/yaml.v3"
)

// InputAdditionalProperties is either a boolean or a schema for arbitrary keys.
// Nil on InputSchema has the usual object meaning: additional keys are allowed.
type InputAdditionalProperties struct {
	Allowed bool
	Schema  *InputSchema
}

func (a InputAdditionalProperties) MarshalYAML() (interface{}, error) {
	if a.Schema != nil {
		return a.Schema, nil
	}
	return a.Allowed, nil
}

func (a *InputAdditionalProperties) UnmarshalYAML(node *yaml.Node) error {
	*a = InputAdditionalProperties{}
	if node.Kind == yaml.ScalarNode && node.Tag == "!!bool" {
		return node.Decode(&a.Allowed)
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("additionalProperties must be a boolean or input schema at %d:%d", node.Line, node.Column)
	}
	a.Schema = &InputSchema{}
	return node.Decode(a.Schema)
}

func (a InputAdditionalProperties) MarshalJSON() ([]byte, error) {
	if a.Schema != nil {
		return json.Marshal(a.Schema)
	}
	return json.Marshal(a.Allowed)
}

func (a *InputAdditionalProperties) UnmarshalJSON(data []byte) error {
	*a = InputAdditionalProperties{}
	if string(data) == "true" || string(data) == "false" {
		return json.Unmarshal(data, &a.Allowed)
	}
	if len(data) == 0 || data[0] != '{' {
		return fmt.Errorf("additionalProperties must be a boolean or input schema")
	}
	a.Schema = &InputSchema{}
	return json.Unmarshal(data, a.Schema)
}

// The recipe reflector intentionally expands structs. Explicit recursion keeps
// nested input contracts finite in the generated authoring schema.
func (InputSchema) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Ref: "#/$defs/RecipeInputSchema"}
}

func recipeInputJSONSchema() *jsonschema.Schema {
	fields := jsonschema.NewProperties()
	fields.Set("type", &jsonschema.Schema{Type: "string"})
	fields.Set("description", &jsonschema.Schema{Type: "string"})
	fields.Set("required", &jsonschema.Schema{Type: "boolean"})
	fields.Set("secret", &jsonschema.Schema{Type: "boolean"})
	fields.Set("object_type", &jsonschema.Schema{Type: "string"})
	fields.Set("default_value", &jsonschema.Schema{})
	fields.Set("properties", &jsonschema.Schema{Type: "object", AdditionalProperties: InputSchema{}.JSONSchema()})
	fields.Set("items", InputSchema{}.JSONSchema())
	fields.Set("additionalProperties", &jsonschema.Schema{OneOf: []*jsonschema.Schema{
		{Type: "boolean"}, InputSchema{}.JSONSchema(),
	}})
	return &jsonschema.Schema{Type: "object", Properties: fields, AdditionalProperties: jsonschema.TrueSchema}
}
