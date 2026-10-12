package recipe

import (
	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestConfigurationRecipeInputConstraints(t *testing.T) {
	schema := RecipeMetadata{InputSchema: map[string]InputSchema{"flag": {Type: "boolean", Default: "${{ context.config.flag }}"}}}
	values, err := schema.ValidateInputShapeAndFillDefaults(nil)
	require.NoError(t, err)
	ref, ok, err := configuration.Parse(values["flag"])
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "boolean", ref.ExpectedType)
	_, err = schema.ValidateInputShapeAndFillDefaults(values)
	require.NoError(t, err)
	schema.InputSchema["flag"] = InputSchema{Type: "string"}
	_, err = schema.ValidateInputShapeAndFillDefaults(values)
	require.ErrorContains(t, err, "conflicting config type")
	schema.InputSchema["flag"] = InputSchema{Type: "artifact"}
	_, err = schema.ValidateInputShapeAndFillDefaults(values)
	require.ErrorContains(t, err, "simple value")
	schema.InputSchema["flag"] = InputSchema{Type: "string", Secret: true}
	_, err = schema.ValidateInputShapeAndFillDefaults(values)
	require.Error(t, err)
}
