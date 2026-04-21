package extensions

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildInputDefaultsAppliesNestedDefaults(t *testing.T) {
	raw := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref": map[string]any{
				"type":    "string",
				"default": "main",
			},
			"config": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"host": map[string]any{
						"type":    "string",
						"default": "localhost",
					},
					"labels": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"name": map[string]any{
									"type":    "string",
									"default": "default-name",
								},
							},
						},
					},
				},
			},
			"tags": map[string]any{
				"type":    "array",
				"items":   map[string]any{"type": "string"},
				"default": []any{"triage"},
			},
		},
	}

	_, compiled, err := parseSchema(raw)
	require.NoError(t, err)

	defaults, err := BuildInputDefaults(raw, compiled)
	require.NoError(t, err)
	require.NotNil(t, defaults)

	input := map[string]interface{}{
		"ref": nil,
		"config": map[string]interface{}{
			"labels": []interface{}{
				map[string]interface{}{},
			},
		},
	}

	changed, err := defaults.Apply(input)
	require.NoError(t, err)
	require.True(t, changed)

	require.Contains(t, input, "tags")
	require.Nil(t, input["ref"], "explicit null should not be overwritten by defaults")

	config, ok := input["config"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "localhost", config["host"])

	labels, ok := config["labels"].([]interface{})
	require.True(t, ok)
	require.Len(t, labels, 1)

	label0, ok := labels[0].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "default-name", label0["name"])

	require.Equal(t, []interface{}{"triage"}, input["tags"])
}

func TestBuildInputDefaultsRejectsUnsupportedCombinatorDefaults(t *testing.T) {
	raw := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{
				"oneOf": []any{
					map[string]any{
						"type":    "string",
						"default": "foo",
					},
					map[string]any{
						"type": "integer",
					},
				},
			},
		},
	}

	_, compiled, err := parseSchema(raw)
	require.NoError(t, err)

	_, err = BuildInputDefaults(raw, compiled)
	require.Error(t, err)
	require.ErrorContains(t, err, "unsupported schema location")
}

func TestBuildInputDefaultsRejectsInvalidDefaultValue(t *testing.T) {
	raw := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"count": map[string]any{
				"type":    "integer",
				"default": "nope",
			},
		},
	}

	_, compiled, err := parseSchema(raw)
	require.NoError(t, err)

	_, err = BuildInputDefaults(raw, compiled)
	require.Error(t, err)
	require.ErrorContains(t, err, "default failed schema validation")
}
