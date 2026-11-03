package recipe

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const structuredRecipe = `schema_version: 2
id: structured
version: "release-17"
input_schema:
  annotations:
    type: array
    default_value: []
    items:
      type: object
      additionalProperties: false
      properties:
        path: {type: string, required: true}
        title: {type: string, default_value: Untitled}
  labels:
    type: object
    default_value: {}
    additionalProperties: {type: string}
  options:
    type: object
    default_value: {}
    properties:
      enabled: {type: boolean, default_value: false}
  free: {type: object}
  session: {type: checkpoint, object_type: test.session/v1}
sequence: []
`

func TestStructuredInputsDefaultsAndSerialization(t *testing.T) {
	rec, err := LoadRecipeFromString([]byte(structuredRecipe))
	require.NoError(t, err)
	require.Equal(t, "release-17", rec.GetMetadata().Version)
	meta := rec.GetMetadata()
	input := map[string]any{
		"annotations": []any{map[string]any{"path": "design.md"}},
		"labels":      map[string]string{"component": "api"},
		"free":        map[string]any{"nested": []any{true, nil, "text"}},
		"session":     objects.ValidationRef("test.session/v1"),
	}
	got, err := meta.ValidateInputShapeAndFillDefaults(input)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"path": "design.md", "title": "Untitled"}, got["annotations"].([]any)[0])
	require.Equal(t, map[string]any{"enabled": false}, got["options"])
	require.NotContains(t, input["annotations"].([]any)[0], "title", "validation must not mutate caller values")
	require.Empty(t, meta.InputSchema["options"].Default, "nested defaults must not mutate the schema")
	got["options"].(map[string]any)["enabled"] = true
	again, err := meta.ValidateInputShapeAndFillDefaults(nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"enabled": false}, again["options"])
	require.Equal(t, []any{}, again["annotations"])
	require.Equal(t, map[string]any{}, again["labels"])

	raw, err := yaml.Marshal(rec)
	require.NoError(t, err)
	restored, err := LoadInternalRecipeFromString(raw)
	require.NoError(t, err)
	require.Equal(t, meta, restored.GetMetadata())
	encoded, err := json.Marshal(input)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	before, err := meta.ValidateInputShapeAndFillDefaults(input)
	require.NoError(t, err)
	after, err := restored.GetMetadata().ValidateInputShapeAndFillDefaults(decoded)
	require.NoError(t, err)
	beforeJSON, err := json.Marshal(before)
	require.NoError(t, err)
	afterJSON, err := json.Marshal(after)
	require.NoError(t, err)
	require.JSONEq(t, string(beforeJSON), string(afterJSON))

	// Struct JSON is used for hashing as well as YAML for source persistence.
	var copied RecipeMetadata
	encoded, err = json.Marshal(meta)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(encoded, &copied))
	after, err = copied.ValidateInputShapeAndFillDefaults(decoded)
	require.NoError(t, err)
	afterJSON, err = json.Marshal(after)
	require.NoError(t, err)
	require.JSONEq(t, string(beforeJSON), string(afterJSON))
	require.NoError(t, Validate(structuredRecipe))
}

func TestStructuredInputErrorPaths(t *testing.T) {
	rec, err := LoadRecipeFromString([]byte(structuredRecipe))
	require.NoError(t, err)
	for _, tc := range []struct {
		name  string
		input map[string]any
		path  string
	}{
		{"required", map[string]any{"annotations": []any{map[string]any{}}}, "annotations[0].path"},
		{"item type", map[string]any{"annotations": []any{map[string]any{"path": 7}}}, "annotations[0].path"},
		{"unknown field", map[string]any{"annotations": []any{map[string]any{"path": "p", "typo": true}}}, "annotations[0].typo"},
		{"map values", map[string]any{"labels": map[string]any{"priority": 7}}, "labels.priority"},
		{"array null", map[string]any{"annotations": nil}, "annotations"},
		{"object null", map[string]any{"labels": nil}, "labels"},
		{"nonstring keys", map[string]any{"labels": map[int]string{1: "x"}}, "labels"},
		{"checkpoint is not ordinary data", map[string]any{"free": objects.ValidationRef("test.session/v1")}, "free"},
		{"ordinary data is not checkpoint", map[string]any{"session": map[string]any{}}, "session"},
		{"unknown root", map[string]any{"unknown": true}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rec.GetMetadata().ValidateInputShapeAndFillDefaults(tc.input)
			require.ErrorContains(t, err, tc.path)
		})
	}
}

func TestRecipeSchemaVersionsKeepObjectMeaningsSeparate(t *testing.T) {
	for _, prefix := range []string{"", "schema_version: 1\n", "schema_version: 2\n"} {
		t.Run(strings.TrimSpace(prefix), func(t *testing.T) {
			rec, err := LoadRecipeFromString([]byte(prefix + "id: sample\ninput_schema:\n  data: {type: object}\nsequence: []\n"))
			require.NoError(t, err)
			isV2 := strings.Contains(prefix, "2")
			_, dataErr := rec.GetMetadata().ValidateInputShapeAndFillDefaults(map[string]any{"data": map[string]any{"key": "value"}})
			_, checkpointErr := rec.GetMetadata().ValidateInputShapeAndFillDefaults(map[string]any{"data": objects.ValidationRef("test.session/v1")})
			if isV2 {
				require.NoError(t, dataErr)
				require.Error(t, checkpointErr)
			} else {
				require.Error(t, dataErr)
				require.NoError(t, checkpointErr)
			}
			raw, err := yaml.Marshal(rec)
			require.NoError(t, err)
			require.Contains(t, string(raw), "type: object", "compilation must not rewrite source schemas")
			if prefix == "" {
				require.NotContains(t, string(raw), "schema_version")
				encoded, err := json.Marshal(rec)
				require.NoError(t, err)
				require.NotContains(t, string(encoded), "schema_version")
			}
		})
	}
	for _, version := range []string{"0", "3", "-1", "null", "true", "'2'", "2.0"} {
		_, err := LoadRecipeFromString([]byte("schema_version: " + version + "\nsequence: []\n"))
		require.ErrorContains(t, err, "schema_version", version)
	}
	legacy, err := LoadRecipeFromString([]byte("input_schema: {data: {type: array}}\nsequence: []\n"))
	require.NoError(t, err)
	_, err = legacy.GetMetadata().ValidateInputShapeAndFillDefaults(map[string]any{"data": []any{}})
	require.ErrorContains(t, err, "unsupported schema type: array")
}

func TestStructuredSchemaDeclarationsAndDefaults(t *testing.T) {
	for _, tc := range []struct{ schema, want string }{
		{"{type: map}", "unsupported schema type"},
		{"{type: string, items: {type: string}}", "items requires"},
		{"{type: array, properties: {a: {type: string}}}", "properties and additionalProperties require"},
		{"{type: object, object_type: test.session/v1}", "object_type requires"},
		{"{type: object, default_value: {a: wrong}, properties: {a: {type: number}}}", "data.a"},
		{"{type: array, default_value: [false], items: {type: string}}", "data[0]"},
		{"{type: object, additionalProperties: 7}", "additionalProperties must"},
		{"{type: array, items: {type: string, secret: true, default_value: 7}}", "default must be a string"},
	} {
		_, err := LoadRecipeFromString([]byte("schema_version: 2\ninput_schema:\n  data: " + tc.schema + "\nsequence: []\n"))
		require.ErrorContains(t, err, tc.want, tc.schema)
	}
}

func TestStructuredInputsPreserveDeclaredReferences(t *testing.T) {
	rec, err := LoadRecipeFromString([]byte(`schema_version: 2
input_schema:
  data:
    type: object
    properties:
      token: {type: string, secret: true}
      setting: {type: boolean}
      session: {type: checkpoint, object_type: test.session/v1}
sequence: []
`))
	require.NoError(t, err)
	input := map[string]any{"data": map[string]any{
		"token":   "${{ context.secrets.token }}",
		"setting": "${{ context.config.enabled }}",
		"session": objects.ValidationRef("test.session/v1"),
	}}
	out, err := rec.GetMetadata().ValidateInputShapeAndFillDefaults(input)
	require.NoError(t, err)
	require.IsType(t, secrets.Reference{}, out["data"].(map[string]any)["token"])
	_, err = rec.GetMetadata().ValidateInputShapeAndFillDefaults(map[string]any{"data": map[string]any{"token": "plaintext"}})
	require.ErrorContains(t, err, "not plaintext")
	_, err = rec.GetMetadata().ValidateInputShapeAndFillDefaults(map[string]any{"data": map[string]any{"extra": secrets.Reference{Name: "token"}}})
	require.ErrorContains(t, err, "data.extra")
}
