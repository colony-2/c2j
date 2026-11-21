package recipe

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/stretchr/testify/require"
)

func TestRecipeSubmissionInputsDeclarations(t *testing.T) {
	for _, version := range []int{0, 1, 2} {
		for declared := 0; declared < 4; declared++ {
			for supplied := 0; supplied < 4; supplied++ {
				t.Run(fmt.Sprintf("v%d/declared=%d/supplied=%d", version, declared, supplied), func(t *testing.T) {
					meta := RecipeMetadata{SchemaVersion: version, InputSchema: map[string]InputSchema{"value": {Type: "string", Required: true}}}
					raw := map[string]any{"value": "run"}
					want := map[string]any{"value": "run"}
					for i, key := range []string{"title", "prompt"} {
						if declared&(1<<i) != 0 {
							meta.InputSchema[key] = InputSchema{Type: "string"}
						}
						if supplied&(1<<i) != 0 {
							raw[key] = "  Café\n" + key + "  "
							if declared&(1<<i) != 0 {
								want[key] = raw[key]
							}
						}
					}
					got, err := meta.ValidateInputShapeAndFillDefaults(raw)
					require.NoError(t, err)
					require.Equal(t, want, got)
				})
			}
		}
	}
}

func TestRecipeSubmissionInputsEmptySchemas(t *testing.T) {
	for _, version := range []int{0, 1, 2} {
		for _, schema := range []map[string]InputSchema{nil, {}} {
			for _, raw := range []map[string]any{nil, {}, {"title": "label"}, {"prompt": "description"}, {"title": "label", "prompt": "description"}} {
				got, err := (RecipeMetadata{SchemaVersion: version, InputSchema: schema}).ValidateInputShapeAndFillDefaults(raw)
				require.NoError(t, err)
				require.Empty(t, got)
			}
		}
	}
}

func TestRecipeSubmissionInputsValuesAndCompatibility(t *testing.T) {
	for _, key := range []string{"title", "prompt"} {
		t.Run(key, func(t *testing.T) {
			empty := RecipeMetadata{}
			for _, value := range []any{nil, 42, map[string]any{"nested": []any{"value"}}, secrets.Reference{Name: "unresolved"}, "${{ context.secrets.unresolved }}", "${{ context.config.unresolved }}"} {
				got, err := empty.ValidateInputShapeAndFillDefaults(map[string]any{key: value})
				require.NoError(t, err)
				require.Empty(t, got)
			}
			meta := RecipeMetadata{InputSchema: map[string]InputSchema{key: {Type: "string", Required: true}}}
			for _, raw := range []map[string]any{nil, {}, {key: 17}, {key: nil}} {
				_, err := meta.ValidateInputShapeAndFillDefaults(raw)
				require.Error(t, err)
			}
			meta.InputSchema[key] = InputSchema{Type: "string", Default: "default"}
			got, err := meta.ValidateInputShapeAndFillDefaults(nil)
			require.NoError(t, err)
			require.Equal(t, "default", got[key])
			got, err = meta.ValidateInputShapeAndFillDefaults(map[string]any{key: "${{ context.config.name }}"})
			require.NoError(t, err)
			_, ok, err := configuration.Parse(got[key])
			require.NoError(t, err)
			require.True(t, ok)
			meta.InputSchema[key] = InputSchema{Type: "number"}
			got, err = meta.ValidateInputShapeAndFillDefaults(map[string]any{key: 17})
			require.NoError(t, err)
			require.Equal(t, 17, got[key])
			meta.InputSchema[key] = InputSchema{Type: "string", Secret: true, Required: true}
			_, err = meta.ValidateInputShapeAndFillDefaults(map[string]any{key: "plaintext"})
			require.Error(t, err)
			_, err = meta.ValidateInputShapeAndFillDefaults(map[string]any{key: secrets.Reference{Name: "private"}})
			require.NoError(t, err)
			contract, err := CompileInputSchema(2, nil)
			require.NoError(t, err)
			_, err = contract.Apply(map[string]any{key: "value"})
			require.ErrorContains(t, err, key)
		})
	}
	for _, key := range []string{"extra", "type", "Title", "Prompt"} {
		_, err := (RecipeMetadata{}).ValidateInputShapeAndFillDefaults(map[string]any{"title": "label", "prompt": "description", key: "value"})
		require.ErrorContains(t, err, key)
	}
	_, err := (RecipeMetadata{SchemaVersion: 3}).ValidateInputShapeAndFillDefaults(map[string]any{"title": "label"})
	require.ErrorContains(t, err, "schema_version")
}

func TestRecipeSubmissionInputsDoNotMutate(t *testing.T) {
	rec, err := LoadRecipeFromString([]byte(`schema_version: 2
id: immutable
input_schema:
  data:
    type: object
    default_value: {}
    properties:
      title: {type: string, default_value: Default}
sequence: []
`))
	require.NoError(t, err)
	meta := rec.GetMetadata()
	beforeRecipe, err := json.Marshal(rec)
	require.NoError(t, err)
	digest := sha256.Sum256(beforeRecipe)
	for _, fail := range []bool{false, true} {
		raw := map[string]any{"data": map[string]any{}, "title": map[string]any{"nested": []any{true}}, "prompt": "text"}
		if fail {
			raw["extra"] = false
		}
		before, err := json.Marshal(raw)
		require.NoError(t, err)
		got, err := meta.ValidateInputShapeAndFillDefaults(raw)
		if fail {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			got["data"].(map[string]any)["title"] = "changed"
		}
		after, err := json.Marshal(raw)
		require.NoError(t, err)
		require.Equal(t, before, after)
	}
	got, err := meta.ValidateInputShapeAndFillDefaults(nil)
	require.NoError(t, err)
	got["data"].(map[string]any)["title"] = "changed default"
	again, err := meta.ValidateInputShapeAndFillDefaults(nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"title": "Default"}, again["data"])
	afterRecipe, err := json.Marshal(rec)
	require.NoError(t, err)
	require.Equal(t, beforeRecipe, afterRecipe)
	require.Equal(t, digest, sha256.Sum256(afterRecipe))
}
