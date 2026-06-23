package ops

import (
	"fmt"
	"reflect"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/invopop/jsonschema"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

// DefaultSchemaGenerator uses invopop/jsonschema for schema generation
type DefaultSchemaGenerator struct {
	reflector *jsonschema.Reflector
}

// NewDefaultSchemaGenerator creates a new schema generator
func NewDefaultSchemaGenerator() *DefaultSchemaGenerator {
	reflector := &jsonschema.Reflector{
		// Don't allow additional properties by default
		AllowAdditionalProperties: false,
		// Anonymous fields should be expanded
		ExpandedStruct: true,
	}

	return &DefaultSchemaGenerator{
		reflector: reflector,
	}
}

// GenerateSchema creates a JSON schema from a Go type
func (g *DefaultSchemaGenerator) GenerateSchema(typ reflect.Type) (*jsonschema.Schema, error) {
	// Handle nil types
	if typ == nil {
		return &jsonschema.Schema{
			Type:                 "object",
			AdditionalProperties: &jsonschema.Schema{},
		}, nil
	}

	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	if typ == reflect.TypeOf(jobdb.ArtifactKey{}) {
		props := orderedmap.New[string, *jsonschema.Schema]()
		props.Set("jobId", &jsonschema.Schema{Type: "string"})
		props.Set("taskOrdinal", &jsonschema.Schema{Type: "integer"})
		props.Set("name", &jsonschema.Schema{Type: "string"})
		props.Set("sizeBytes", &jsonschema.Schema{Type: "integer"})
		return &jsonschema.Schema{
			Type:       "object",
			Properties: props,
			Required:   []string{"jobId", "taskOrdinal", "name"},
		}, nil
	}

	// Handle interface types
	if typ.Kind() == reflect.Interface {
		// For interface{} types, return a schema that accepts any type
		return &jsonschema.Schema{
			Type:                 "object",
			AdditionalProperties: &jsonschema.Schema{},
		}, nil
	}

	// Handle map types
	if typ.Kind() == reflect.Map {
		// For map[string]interface{} types, return a flexible object schema
		return &jsonschema.Schema{
			Type:                 "object",
			AdditionalProperties: &jsonschema.Schema{},
		}, nil
	}

	// Create a value from the type
	val := reflect.New(typ).Interface()

	// Use invopop/jsonschema to generate the schema, guarding against panics.
	schema, err := safeReflectSchema(g.reflector, val)
	if err != nil {
		return &jsonschema.Schema{
			Type:                 "object",
			AdditionalProperties: &jsonschema.Schema{},
		}, nil
	}
	return schema, nil
}

func safeReflectSchema(reflector *jsonschema.Reflector, value interface{}) (schema *jsonschema.Schema, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("schema reflection failed: %v", r)
		}
	}()
	return reflector.Reflect(value), nil
}

// ValidateStructTags ensures all fields have explicit json tags
func (g *DefaultSchemaGenerator) ValidateStructTags(typ reflect.Type) error {
	// Handle nil types
	if typ == nil {
		return nil
	}

	// Handle interface and map types - they don't need validation
	if typ.Kind() == reflect.Interface || typ.Kind() == reflect.Map {
		return nil
	}

	// Ensure type is a struct
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	if typ.Kind() != reflect.Struct {
		return nil // Non-structs don't need json tags
	}

	// Check each field has an explicit json tag
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Check for json tag
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" {
			return fmt.Errorf("field %s.%s is missing required json tag", typ.Name(), field.Name)
		}

		// If field is "-", it's explicitly ignored, which is fine
		if jsonTag == "-" {
			continue
		}

		// Recursively check nested structs
		fieldType := field.Type
		if fieldType.Kind() == reflect.Slice || fieldType.Kind() == reflect.Array {
			fieldType = fieldType.Elem()
		}
		if fieldType.Kind() == reflect.Map {
			fieldType = fieldType.Elem()
		}
		if fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
		}

		// Only validate struct types
		if fieldType.Kind() == reflect.Struct {
			// Skip time.Time and other standard library types
			if fieldType.PkgPath() == "" || fieldType.PkgPath() == "time" ||
				fieldType.PkgPath() == "encoding/json" {
				continue
			}

			if err := g.ValidateStructTags(fieldType); err != nil {
				return err
			}
		}
	}

	return nil
}
