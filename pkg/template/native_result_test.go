package template

import (
	"encoding/json"
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestNestedStateOutputRetainsDependencyDataWhenSerialized(t *testing.T) {
	ctx := newStateMachineCtx(t, newRecipeCtx(t, nil), "workflow", map[string]any{})
	evidence := recipeartifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "child", TaskOrdinal: 7, Name: "verification.json", SizeBytes: 42})
	dependencies := map[string]any{"child": map[string]any{
		"status": "completed", "artifacts": map[string]recipeartifacts.Ref{"verification.json": evidence},
		"large": int64(9007199254740993), "checks": []any{map[string]any{"ok": true}},
	}}
	addStateOutput(t, ctx, "implementation", map[string]any{"dependencies": dependencies})
	value, err := ctx.resolveTemplate(`${{ {"implementation": state_output("implementation", "dependencies", {}), "missing": state_output("missing", "dependencies", {}), "history": [state_output("implementation", "dependencies", {})]} }}`)
	require.NoError(t, err)
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	want, err := json.Marshal(map[string]any{"implementation": dependencies, "missing": map[string]any{}, "history": []any{dependencies}})
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(raw))
	require.Contains(t, string(raw), "9007199254740993", "conversion must not round integers")
}

func TestNativeTemplateResultPreservesNativeArtifactsAndNullContainers(t *testing.T) {
	evidence := recipeartifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "child", TaskOrdinal: 7, Name: "verification.json", SizeBytes: 42})
	value := map[string]any{
		"artifacts": map[string]recipeartifacts.Ref{"verification.json": evidence},
		"empty_map": map[string]any(nil), "empty_list": []any(nil),
	}
	require.Equal(t, value, nativeTemplateResult(value))
}
