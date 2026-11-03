package recipe

import (
	"encoding/json"
	"fmt"
	"reflect"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// InputSchema defines the schema for an input parameter
type InputSchema struct {
	// sourceType preserves diagnostics when an adapter maps an unsupported type.
	sourceType           string
	Secret               bool                       `yaml:"secret,omitempty"`
	ObjectType           string                     `yaml:"object_type,omitempty"`
	Type                 string                     `yaml:"type,omitempty"`          // Type of the input (string, number, boolean, etc.)
	Description          string                     `yaml:"description,omitempty"`   // Description of the input
	Required             bool                       `yaml:"required,omitempty"`      // Whether the input is required
	Default              interface{}                `yaml:"default_value,omitempty"` // Default value if not provided
	Properties           map[string]InputSchema     `yaml:"properties,omitempty" json:"properties,omitempty"`
	Items                *InputSchema               `yaml:"items,omitempty" json:"items,omitempty"`
	AdditionalProperties *InputAdditionalProperties `yaml:"additionalProperties,omitempty" json:"additionalProperties,omitempty"`
}

func (def InputSchema) validate(key string, value interface{}) error {
	if def.Secret {
		if _, ok, err := secrets.Parse(value); ok || err != nil {
			return err
		}
		return fmt.Errorf("secret input %q requires a reference, not plaintext", key)
	}
	if _, ok, err := configuration.Parse(value); ok || err != nil {
		return err
	}
	if secrets.Contains(value) {
		return fmt.Errorf("input %q is not marked secret", key)
	}

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
	case "checkpoint":
		ref, ok, err := objects.Parse(value)
		if err != nil {
			return fmt.Errorf("field %q: %w", key, err)
		}
		if !ok {
			return fmt.Errorf("field %q requires an object checkpoint", key)
		}
		if def.ObjectType != "" && ref.Type != def.ObjectType {
			return fmt.Errorf("field %q expects object type %q, got %q", key, def.ObjectType, ref.Type)
		}
		return nil
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
		return fmt.Errorf("field '%s' has an unsupported schema type: %s", key, def.typeName())
	}

	if valueType == expectedGoType {
		return nil
	}
	return fmt.Errorf("field '%s' has wrong type: expected '%s' (Go type: %s) but got '%s' (Go type: %s)",
		key, def.Type, expectedGoType, value, valueType)
}

// Artifact inputs cross a JSON boundary when a job is persisted. Accept both
// native values and their serialized references without changing producer keys.
func isArtifactValue(value interface{}) bool {
	if _, ok, err := recipeartifacts.ParseValue(value); ok {
		return err == nil
	}
	switch v := value.(type) {
	case jobdb.ArtifactKey:
		return v.Validate() == nil
	case *jobdb.ArtifactKey:
		return v != nil && v.Validate() == nil
	case recipeartifacts.Ref:
		return v.Validate() == nil
	case *recipeartifacts.Ref:
		return v != nil && v.Validate() == nil
	case map[string]interface{}:
		raw, err := json.Marshal(v)
		if err != nil {
			return false
		}
		if _, ok := v["kind"]; ok {
			var ref recipeartifacts.Ref
			return json.Unmarshal(raw, &ref) == nil && ref.Validate() == nil
		}
		if _, ok := v["taskOrdinal"]; !ok {
			return false
		}
		var key jobdb.ArtifactKey
		return json.Unmarshal(raw, &key) == nil && key.Validate() == nil
	default:
		return false
	}
}

func isArtifactMapValue(value interface{}) bool {
	if value == nil {
		return false
	}
	v := reflect.ValueOf(value)
	if v.Kind() != reflect.Map || v.Type().Key().Kind() != reflect.String {
		return false
	}
	iter := v.MapRange()
	for iter.Next() {
		if !isArtifactValue(iter.Value().Interface()) {
			return false
		}
	}
	return true
}

func (def InputSchema) validateMissing(key string) error {
	if def.Required && def.Default == nil {
		return fmt.Errorf("required field '%s' is missing", key)
	}
	return nil
}

// ValidateSecretDeclaration checks the public default without resolving it.
func (def InputSchema) ValidateSecretDeclaration() error {
	if !def.Secret {
		return nil
	}
	if def.Type != "string" {
		return fmt.Errorf("secret input requires string type")
	}
	if def.Default != nil {
		return secrets.ValidateDefault(def.Default)
	}
	return nil
}

func (def InputSchema) typeName() string {
	if def.sourceType != "" {
		return def.sourceType
	}
	return def.Type
}
