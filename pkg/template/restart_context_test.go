package template

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/task"
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
	require.NoError(t, seq.ApplyContextPatch(task.ContextPatch{Scopes: []task.ScopePatch{{Container: "sequence", ID: "prior", Outputs: map[string]any{"remove": nil, "array": []any{9}, "nested": map[string]any{"remove": nil}}}, {Container: "sequence", ID: "new", Outputs: map[string]any{"created": true}}}}))
	out := seq.TemplateData.Sequence["prior"].Outputs
	require.NotContains(t, out, "remove")
	require.Equal(t, []any{9}, out["array"])
	require.Equal(t, map[string]any{"keep": true}, out["nested"])
	require.True(t, seq.TemplateData.Sequence["new"].Outputs["created"].(bool))
}
