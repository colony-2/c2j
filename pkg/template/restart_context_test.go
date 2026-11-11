package template

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestRestartSnapshotPreservesDeferredReferences(t *testing.T) {
	key := jobdb.ArtifactKey{JobId: "source", Name: "unavailable"}
	input := map[string]any{"configuration": configuration.Ref("unresolved"), "object": objects.Ref{Format: "v1", Type: "test", TenantID: "tenant", Artifact: key}, "artifact": artifacts.NewStoredRef(key)}
	rc, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, input, contextual.JobContext{})
	require.NoError(t, err)
	job, visible, err := rc.RestartContextSnapshot()
	require.NoError(t, err)
	require.NotNil(t, job)
	expected, err := json.Marshal(input)
	require.NoError(t, err)
	actual, err := json.Marshal(visible["inputs"])
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(actual))
	for _, name := range []string{"inputs", "sequence", "states", "vars", "outputs", "transition", "failure", "scope", "context", "item", "index"} {
		require.Contains(t, visible, name)
	}
}
func TestMergePatchDeletionArrayReplacementAndNewOutput(t *testing.T) {
	rc, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, map[string]any{}, contextual.JobContext{})
	require.NoError(t, err)
	seq, err := rc.NewChildContext(ScopeSequence, recipe.NodeMetadata{ID: "s"}, "", map[string]any{})
	require.NoError(t, err)
	seq.TemplateData.Sequence["prior"] = StepOutput{Outputs: map[string]any{"remove": "gone", "array": []any{1, 2, 3}, "nested": map[string]any{"keep": true, "remove": true}}}
	require.NoError(t, seq.applyLegacyContextPatch(legacyContextPatch{Scopes: []legacyScopePatch{{Container: "sequence", ID: "prior", Outputs: map[string]any{"remove": nil, "array": []any{9}, "nested": map[string]any{"remove": nil}}}, {Container: "sequence", ID: "new", Outputs: map[string]any{"created": true}}}}))
	out := seq.TemplateData.Sequence["prior"].Outputs
	require.NotContains(t, out, "remove")
	require.Equal(t, []any{9}, out["array"])
	require.Equal(t, map[string]any{"keep": true}, out["nested"])
	require.True(t, seq.TemplateData.Sequence["new"].Outputs["created"].(bool))
}

// NUM-01: compare number tokens, never float64-based JSONEq.
func TestRestartSnapshotExactNumbersDetached(t *testing.T) {
	const raw = `{"n":9007199254740993,"array":[9007199254740995,18446744073709551617,1.23000000000000000001]}`
	var values map[string]any
	require.NoError(t, decodePatchDocument([]byte(raw), &values))
	rc, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, values, contextual.JobContext{})
	require.NoError(t, err)
	rc, err = rc.NewChildContext(ScopeSequence, recipe.NodeMetadata{ID: "scope"}, "", map[string]any{})
	require.NoError(t, err)
	rc.TemplateData.Sequence["seed"] = StepOutput{Outputs: values}
	_, visible, err := rc.RestartContextSnapshot()
	require.NoError(t, err)
	out := visible["sequence"].(map[string]any)["seed"].(map[string]any)["outputs"].(map[string]any)
	require.Equal(t, json.Number("9007199254740993"), out["n"])
	require.Equal(t, values, out)
	out["array"].([]any)[0] = "changed"
	require.Equal(t, json.Number("9007199254740995"), values["array"].([]any)[0])
}

func TestRestartExactNumbersCELConversion(t *testing.T) {
	rc, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, map[string]any{
		"integer": json.Number("9007199254740993"),
		"decimal": json.Number("1.23000000000000000001"),
		"huge":    json.Number("18446744073709551617"),
		"opaque":  map[string]any{"decimal": json.Number("1.23000000000000000001")},
	}, contextual.JobContext{})
	require.NoError(t, err)
	got, err := rc.resolveValue("${{ inputs.integer + 1 }}")
	require.NoError(t, err)
	require.Equal(t, int64(9007199254740994), got)
	for _, field := range []string{"decimal", "huge"} {
		_, err := rc.resolveValue("${{ inputs." + field + " }}")
		require.ErrorContains(t, err, "cannot be represented exactly")
	}
	got, err = rc.resolveValue("${{ inputs.opaque }}")
	require.NoError(t, err)
	requirePatchJSON(t, `{"decimal":1.23000000000000000001}`, got)
}

func TestRestartExactNumbersGoTemplateScalar(t *testing.T) {
	var values map[string]any
	require.NoError(t, decodePatchDocument([]byte(`{"small":0,"n":9007199254740993,"huge":18446744073709551617,"decimal":1.23000000000000000001}`), &values))
	rc, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, values, contextual.JobContext{})
	require.NoError(t, err)
	rc, err = rc.NewChildContext(ScopeSequence, recipe.NodeMetadata{ID: "scope"}, "", map[string]any{})
	require.NoError(t, err)
	rc.TemplateData.Sequence["seed"] = StepOutput{Outputs: values}
	for name, expected := range values {
		for _, root := range []string{"sequence.seed.outputs", "inputs"} {
			if root == "inputs" {
				rc.TemplateData.ContainerInputs = values
			}
			actual, err := rc.resolveValue("{{ " + root + "." + name + " }}")
			require.NoError(t, err)
			raw, err := json.Marshal(expected)
			require.NoError(t, err)
			requirePatchJSON(t, string(raw), actual)
		}
	}
}
