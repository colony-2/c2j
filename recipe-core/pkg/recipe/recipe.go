package recipe

import (
	"fmt"
	"reflect"

	"github.com/colony-2/swf-go/pkg/swf"
	yamlv3 "gopkg.in/yaml.v3"
)

type RecipeImpl interface {
	isRecipe()
	GetMetadata() RecipeMetadata
}

type Recipe struct {
	RecipeImpl
}

type RecipeProvider interface {
	GetRecipe(name string) (*Recipe, error)
}

func (r Recipe) GetMetdata() RecipeMetadata {
	switch t := r.RecipeImpl.(type) {
	case *RecipeState:
		return t.RecipeMetadata
	case *RecipeSequence:
		return t.RecipeMetadata
	case *RecipeOp:
		return t.RecipeMetadata
	default:
		panic("invalid recipe type")
	}
}

func (n *Recipe) MarshalYAML() (interface{}, error) {
	return n.RecipeImpl, nil
}

func (n *Recipe) UnmarshalYAML(node *yamlv3.Node) error {
	var raw map[string]interface{}
	if err := node.Decode(&raw); err != nil {
		return err
	}

	var impl RecipeImpl
	switch {
	case raw["state"] != nil:
		impl = &RecipeState{}
	case raw["sequence"] != nil:
		impl = &RecipeSequence{}
	case raw["op"] != nil:
		impl = &RecipeOp{}
	default:
		// update to include line/col

		return fmt.Errorf("root node must either be a op, sequence, state, or shared reference at %d:%d", node.Line, node.Column)
	}

	// Second pass: decode into concrete type
	if err := node.Decode(impl); err != nil {
		return err
	}

	if op, ok := impl.(*RecipeOp); ok {
		err := checkOpInputs(op.Op, op.Inputs, node.Line, node.Column)
		if err != nil {
			return err
		}
	}

	n.RecipeImpl = impl
	return nil
}

type RecipeMetadata struct {
	Version      string `yaml:"version"`
	NodeMetadata `yaml:",inline"`
	Defs         map[string]Node        `yaml:"defs,omitempty"`         // Shared node definitions
	InputSchema  map[string]InputSchema `yaml:"input_schema,omitempty"` // Optional schema for inputs
}

func (n RecipeMetadata) ValidateInputShapeAndFillDefaults(data map[string]interface{}) (map[string]interface{}, error) {

	resolved := make(map[string]interface{})

	// 1. Check for missing required fields and apply defaults
	for key, def := range n.InputSchema {
		_, exists := data[key]

		if !exists {
			err := def.validateMissing(key)
			if err != nil {
				return nil, err
			}
		}

		// Apply default value if field is missing and a default is provided
		if !exists && def.Default != nil {
			resolved[key] = def.Default
		}
	}

	// 2. Check types and handle fields present in data but not in schema
	for key, value := range data {
		resolved[key] = value
		def, schemaExists := n.InputSchema[key]

		if !schemaExists {
			return nil, fmt.Errorf("field '%s' is present in data but not defined in schema", key)
		}

		err := def.validate(key, value)
		if err != nil {
			return nil, err
		}
	}

	return resolved, nil
}

type RecipeSequence struct {
	RecipeMetadata `yaml:",inline" refer:"true"`
	SequenceData   `yaml:",inline" refer:"true"`
}

func (r *RecipeSequence) GetMetadata() RecipeMetadata {
	return r.RecipeMetadata
}

func (r *RecipeSequence) isRecipe() {}

type RecipeState struct {
	RecipeMetadata   `yaml:",inline" refer:"true"`
	StateMachineData `yaml:",inline" refer:"true"`
}

func (r *RecipeState) GetMetadata() RecipeMetadata {
	return r.RecipeMetadata
}

func (r *RecipeState) isRecipe() {}

type RecipeOp struct {
	RecipeMetadata `yaml:",inline" refer:"true"`
	OpData         `yaml:",inline" refer:"true"`
}

func (r *RecipeOp) GetMetadata() RecipeMetadata {
	return r.RecipeMetadata
}

func (r *RecipeOp) isRecipe() {}

// InputSchema defines the schema for an input parameter
type InputSchema struct {
	Type        string      `yaml:"type,omitempty"`                                                // Type of the input (string, number, boolean, etc.)
	Description string      `yaml:"description,omitempty"`                                         // Description of the input
	Required    bool        `yaml:"required,omitempty"`                                            // Whether the input is required
	Default     interface{} `yaml:"default_value,omitempty" jsonschema:"oneof_type=string;number"` // Default value if not provided
}

func (def InputSchema) validate(key string, value interface{}) error {

	if value == nil {
		return fmt.Errorf("field '%s' is nil", key)
	}

	// Get the Go type name of the value provided in the data
	valueType := reflect.TypeOf(value).Kind().String()

	var expectedGoType string
	switch def.Type {
	case "string":
		expectedGoType = "string"
	case "number":
		// Go unmarshaling often uses float64 for generic numbers,
		// so we check for both float64 and int/int64
		if valueType == "float64" || valueType == "int" || valueType == "int64" {
			return nil
		}
		expectedGoType = "number (float64, int, or int64)"
	case "boolean":
		expectedGoType = "bool"
	case "artifact":
		if isArtifactValue(value) {
			return nil
		}
		expectedGoType = "swf.ArtifactKey"
	case "artifact_map":
		if isArtifactMapValue(value) {
			return nil
		}
		expectedGoType = "map[string]swf.ArtifactKey"
	default:
		return fmt.Errorf("field '%s' has an unsupported schema type: %s", key, def.Type)
	}

	if valueType == expectedGoType {
		return nil
	}
	return fmt.Errorf("field '%s' has wrong type: expected '%s' (Go type: %s) but got '%s' (Go type: %s)",
		key, def.Type, expectedGoType, value, valueType)
}

func isArtifactValue(value interface{}) bool {
	switch v := value.(type) {
	case swf.ArtifactKey:
		return true
	case *swf.ArtifactKey:
		return v != nil
	default:
		return false
	}
}

func isArtifactMapValue(value interface{}) bool {
	switch v := value.(type) {
	case map[string]swf.ArtifactKey:
		return true
	case map[string]*swf.ArtifactKey:
		return true
	case map[string]interface{}:
		if len(v) == 0 {
			return true
		}
		for _, entry := range v {
			if !isArtifactValue(entry) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (def InputSchema) validateMissing(key string) error {
	if def.Required && def.Default == nil {
		return fmt.Errorf("required field '%s' is missing", key)
	}
	return nil
}
