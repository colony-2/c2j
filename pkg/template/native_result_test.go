package template

import (
	"encoding/json"
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
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

func TestNativeTemplateResultSerializesCELAndInterfaceMaps(t *testing.T) {
	evidence := recipeartifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "child", TaskOrdinal: 7, Name: "verification.json", SizeBytes: 42})
	for name, value := range map[string]any{
		"CEL map": map[ref.Val]ref.Val{
			types.String("large"):  types.Int(9007199254740993),
			types.String("nested"): types.NewRefValMap(types.DefaultTypeAdapter, map[ref.Val]ref.Val{types.String("ok"): types.True}),
			types.String("empty"):  types.NewRefValMap(types.DefaultTypeAdapter, map[ref.Val]ref.Val{}),
		},
		"interface map": map[any]any{
			"large":  int64(9007199254740993),
			"nested": map[any]any{types.String("ok"): types.True},
			"empty":  map[any]any{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			// Exercise the same nested-map/list shapes persisted by recipe outputs.
			result := nativeTemplateResult(map[string]any{"history": []any{value}, "artifact": evidence})
			history := result.(map[string]any)["history"].([]any)
			require.IsType(t, map[string]any{}, history[0], "string-keyed results must be portable JSON objects")
			raw, err := json.Marshal(result)
			require.NoError(t, err)
			require.Contains(t, string(raw), "9007199254740993")
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(raw, &decoded))
			item := decoded["history"].([]any)[0].(map[string]any)
			require.Equal(t, map[string]any{"ok": true}, item["nested"])
			require.Equal(t, map[string]any{}, item["empty"])
			require.Equal(t, evidence, result.(map[string]any)["artifact"])
		})
	}
}

func TestNativeTemplateResultPreservesNonStringMapKeys(t *testing.T) {
	value := map[ref.Val]ref.Val{types.Int(1): types.String("integer"), types.String("1"): types.String("string")}
	require.Equal(t, map[any]any{int64(1): "integer", "1": "string"}, nativeTemplateResult(value))
}

func TestJSONHelpersNormalizeCELHistoryValues(t *testing.T) {
	ctx := newStateMachineCtx(t, newRecipeCtx(t, nil), "workflow", map[string]any{})
	for _, function := range []string{"json_stringify", "string"} {
		t.Run(function, func(t *testing.T) {
			value, err := ctx.resolveTemplate(`${{ ` + function + `(state_output("missing", "consultations", {})) }}`)
			require.NoError(t, err)
			require.Equal(t, "{}", value)

			value, err = ctx.resolveTemplate(`${{ ` + function + `({"history": [{"large": 9007199254740993, "missing": state_output("missing", "consultations", {}), "value": null}]}) }}`)
			require.NoError(t, err)
			require.Equal(t, `{"history":[{"large":9007199254740993,"missing":{},"value":null}]}`, value)
		})
	}
	value, err := ctx.resolveTemplate(`${{ jq({"history": [{"large": 9007199254740993}]}, ".history[0].large") }}`)
	require.NoError(t, err)
	require.EqualValues(t, int64(9007199254740993), value)
}
