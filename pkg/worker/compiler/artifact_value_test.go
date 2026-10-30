package compiler

import (
	"reflect"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/require"
)

func valueSource(format string, value any) map[string]any {
	return map[string]any{"kind": "value", "format": format, "value": value}
}

func TestValueArtifactBindings(t *testing.T) {
	ctx := artifactBindingResolutionContext(t, nil)
	content := "${{ nonexistent.value }}\n$(touch /not-run)\n"
	ctx.TemplateData.ContainerInputs["body"] = content
	refs, err := resolveArtifactBindings(ctx, map[string]any{"context.txt": valueSource("text", "${{ inputs.body }}")})
	require.NoError(t, err)
	data, err := refs["context.txt"].ValueBytes()
	require.NoError(t, err)
	require.Equal(t, content, string(data))
	require.Equal(t, "context.txt", refs["context.txt"].NameValue())
	for _, name := range []string{"../escape", "/absolute", "dir/", ".", ""} {
		_, err = resolveArtifactBindings(ctx, map[string]any{name: valueSource("text", "x")})
		require.Error(t, err)
	}
	_, err = resolveArtifactBindings(ctx, map[string]any{"a": valueSource("text", "x"), "a/b": valueSource("text", "y")})
	require.ErrorContains(t, err, "conflicts")
	set := map[string]any{}
	for _, name := range []string{"kind", "format", "value"} {
		set[name] = valueSource("text", name)
	}
	refs, err = resolveArtifactBindings(ctx, map[string]any{"docs/": set})
	require.NoError(t, err)
	require.Len(t, refs, 3)
	for _, name := range []string{"kind", "format", "value"} {
		data, err := refs["docs/"+name].ValueBytes()
		require.NoError(t, err)
		require.Equal(t, name, string(data))
	}
	_, err = (recipe.RecipeMetadata{InputSchema: map[string]recipe.InputSchema{"documents": {Type: "artifact_map"}}}).ValidateInputShapeAndFillDefaults(map[string]any{"documents": set})
	require.NoError(t, err)
}

func TestValueArtifactTypedInputs(t *testing.T) {
	type input struct {
		Document  artifacts.Ref            `json:"document"`
		Documents map[string]artifacts.Ref `json:"documents"`
		Ordinary  map[string]any           `json:"ordinary"`
	}
	scalar := valueSource("text", "literal ${{ untouched }}")
	scalar["name"] = "scalar.txt"
	ordinary := valueSource("business-format", map[string]any{"anything": true})
	raw := map[string]any{"document": scalar, "documents": map[string]any{"map.json": valueSource("json", nil)}, "ordinary": ordinary}
	normalized, err := NormalizeOpInput(reflect.TypeOf(input{}), raw)
	require.NoError(t, err)
	require.Empty(t, normalized.StoredArtifactKeys)
	require.Len(t, normalized.ValueArtifacts, 2)
	require.Equal(t, ordinary, normalized.Data["ordinary"])
	var decoded input
	require.NoError(t, ops.DecodeWithJsonTags(normalized.Data, &decoded))
	require.Equal(t, "scalar.txt", decoded.Document.Name)
	require.Equal(t, "map.json", decoded.Documents["map.json"].Name)
	delete(scalar, "name")
	_, err = NormalizeOpInput(reflect.TypeOf(input{}), raw)
	require.ErrorContains(t, err, "requires name")
}
