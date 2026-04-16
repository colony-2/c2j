package ops

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchemaGeneratorValidation(t *testing.T) {
	generator := NewDefaultSchemaGenerator()

	t.Run("struct with all json tags passes", func(t *testing.T) {
		type GoodStruct struct {
			Name    string    `json:"name"`
			Age     int       `json:"age"`
			Active  bool      `json:"active"`
			Created time.Time `json:"created"`
		}

		err := generator.ValidateStructTags(reflect.TypeOf(GoodStruct{}))
		assert.NoError(t, err)
	})

	t.Run("struct missing json tag fails", func(t *testing.T) {
		type BadStruct struct {
			Name   string `json:"name"`
			Age    int    // Missing json tag
			Active bool   `json:"active"`
		}

		err := generator.ValidateStructTags(reflect.TypeOf(BadStruct{}))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Age")
		assert.Contains(t, err.Error(), "missing required json tag")
	})

	t.Run("nested struct validation", func(t *testing.T) {
		type Address struct {
			Street string `json:"street"`
			City   string // Missing json tag
		}

		type Person struct {
			Name    string  `json:"name"`
			Address Address `json:"address"`
		}

		err := generator.ValidateStructTags(reflect.TypeOf(Person{}))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "City")
	})

	t.Run("slice of structs validation", func(t *testing.T) {
		type Item struct {
			ID   string `json:"id"`
			Name string // Missing json tag
		}

		type Container struct {
			Items []Item `json:"items"`
		}

		err := generator.ValidateStructTags(reflect.TypeOf(Container{}))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Name")
	})

	t.Run("map with struct values validation", func(t *testing.T) {
		type Value struct {
			Data string // Missing json tag
		}

		type Container struct {
			Values map[string]Value `json:"values"`
		}

		err := generator.ValidateStructTags(reflect.TypeOf(Container{}))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Data")
	})

	t.Run("pointer to struct validation", func(t *testing.T) {
		type Inner struct {
			Field string // Missing json tag
		}

		type Outer struct {
			Inner *Inner `json:"inner"`
		}

		err := generator.ValidateStructTags(reflect.TypeOf(Outer{}))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Field")
	})

	t.Run("ignored fields are allowed", func(t *testing.T) {
		type StructWithIgnored struct {
			Public     string `json:"public"`
			Ignored    string `json:"-"`
			unexported string // Unexported fields don't need tags
		}

		err := generator.ValidateStructTags(reflect.TypeOf(StructWithIgnored{}))
		assert.NoError(t, err)
	})

	t.Run("standard library types are skipped", func(t *testing.T) {
		type StructWithStdTypes struct {
			Time     time.Time       `json:"time"`
			Duration time.Duration   `json:"duration"`
			Data     json.RawMessage `json:"data"`
		}

		err := generator.ValidateStructTags(reflect.TypeOf(StructWithStdTypes{}))
		assert.NoError(t, err)
	})

	t.Run("non-struct types pass validation", func(t *testing.T) {
		// Primitives don't need json tags
		assert.NoError(t, generator.ValidateStructTags(reflect.TypeOf("string")))
		assert.NoError(t, generator.ValidateStructTags(reflect.TypeOf(123)))
		assert.NoError(t, generator.ValidateStructTags(reflect.TypeOf(true)))
		assert.NoError(t, generator.ValidateStructTags(reflect.TypeOf([]string{})))
		assert.NoError(t, generator.ValidateStructTags(reflect.TypeOf(map[string]string{})))
	})
}

func TestSchemaGeneratorGeneration(t *testing.T) {
	generator := NewDefaultSchemaGenerator()

	t.Run("generate schema for simple struct", func(t *testing.T) {
		// Let's test with TestConfig which is exported
		schema, err := generator.GenerateSchema(reflect.TypeOf(TestConfig{}))
		require.NoError(t, err)
		require.NotNil(t, schema)

		// Convert to JSON to verify structure
		schemaJSON, err := json.Marshal(schema)
		require.NoError(t, err)

		var schemaMap map[string]interface{}
		err = json.Unmarshal(schemaJSON, &schemaMap)
		require.NoError(t, err)

		// Debug: print the schema
		t.Logf("Schema JSON: %s", string(schemaJSON))

		assert.Equal(t, "object", schemaMap["type"])

		properties, ok := schemaMap["properties"].(map[string]interface{})
		require.True(t, ok, "properties not found in schema: %v", schemaMap)

		// Check url property
		urlProp, ok := properties["url"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "string", urlProp["type"])

		// Check timeout property
		timeoutProp, ok := properties["timeout"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "integer", timeoutProp["type"])
	})

	t.Run("generate schema with nested struct", func(t *testing.T) {
		type Address struct {
			Street string `json:"street"`
			City   string `json:"city"`
		}

		type Person struct {
			Name    string  `json:"name"`
			Age     int     `json:"age"`
			Address Address `json:"address"`
		}

		schema, err := generator.GenerateSchema(reflect.TypeOf(Person{}))
		require.NoError(t, err)
		require.NotNil(t, schema)

		schemaJSON, err := json.Marshal(schema)
		require.NoError(t, err)

		var schemaMap map[string]interface{}
		err = json.Unmarshal(schemaJSON, &schemaMap)
		require.NoError(t, err)

		properties, ok := schemaMap["properties"].(map[string]interface{})
		require.True(t, ok)

		// Check that address property exists
		addressProp, ok := properties["address"].(map[string]interface{})
		require.True(t, ok)

		// invopop/jsonschema might use $ref for nested types
		_, hasRef := addressProp["$ref"]
		_, hasType := addressProp["type"]
		assert.True(t, hasRef || hasType, "Address property should have either $ref or type")
	})

	t.Run("generate schema for slice types", func(t *testing.T) {
		type Container struct {
			Items []string `json:"items"`
		}

		schema, err := generator.GenerateSchema(reflect.TypeOf(Container{}))
		require.NoError(t, err)

		schemaJSON, err := json.Marshal(schema)
		require.NoError(t, err)

		var schemaMap map[string]interface{}
		err = json.Unmarshal(schemaJSON, &schemaMap)
		require.NoError(t, err)

		properties, ok := schemaMap["properties"].(map[string]interface{})
		require.True(t, ok)

		itemsProp, ok := properties["items"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "array", itemsProp["type"])

		itemsSchema, ok := itemsProp["items"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "string", itemsSchema["type"])
	})
}
