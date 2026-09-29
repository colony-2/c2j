package recipe

import (
	"encoding/json"
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestArtifactInputsSurviveJobSerialization(t *testing.T) {
	key := jobdb.ArtifactKey{JobId: "child", TaskOrdinal: 3, Name: "design.md", SizeBytes: 42}
	for _, value := range []any{key, recipeartifacts.NewStoredRef(key)} {
		meta := RecipeMetadata{InputSchema: map[string]InputSchema{"document": {Type: "artifact", Required: true}, "documents": {Type: "artifact_map", Required: true}}}
		native := map[string]any{"document": value, "documents": map[string]any{"design": value}}
		_, err := meta.ValidateInputShapeAndFillDefaults(native)
		require.NoError(t, err)
		raw, err := json.Marshal(native)
		require.NoError(t, err)
		var restored map[string]any
		require.NoError(t, json.Unmarshal(raw, &restored))
		actual, err := meta.ValidateInputShapeAndFillDefaults(restored)
		require.NoError(t, err)
		require.Equal(t, restored, actual)
	}
	for _, value := range []any{nil, map[string]any{}, map[string]any{"jobId": "x", "name": "x"}, map[string]any{"kind": "stored"}, map[string]any{"jobId": "x", "taskOrdinal": -1, "name": "x"}} {
		require.False(t, isArtifactValue(value), "%#v", value)
	}
}
