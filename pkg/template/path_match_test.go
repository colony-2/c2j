package template

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestPathMatchTemplates(t *testing.T) {
	for _, inventory := range []any{[]string{"design.md"}, []any{"design.md"}} {
		ctx := newSequenceCtx(t, newRecipeCtx(t, nil), "match", map[string]any{
			"reference": "outbox/design.md", "inventory": inventory,
		})
		result, err := ctx.resolveTemplate(`${{ path_match(inputs.reference, inputs.inventory) }}`)
		require.NoError(t, err)
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		require.JSONEq(t, `{"status":"matched","path":"design.md","candidates":["design.md"]}`, string(encoded))
		result, err = ctx.resolveTemplate(`status={{ (path_match inputs.reference inputs.inventory).status }} path={{ (path_match inputs.reference inputs.inventory).path }}`)
		require.NoError(t, err)
		require.Equal(t, "status=matched path=design.md", result)
	}
}

func TestPathMatchTemplateTypeErrors(t *testing.T) {
	for _, inventory := range []any{nil, "design.md", []any{"design.md", 1}, []any{"design.md", nil}} {
		ctx := newSequenceCtx(t, newRecipeCtx(t, nil), "match", map[string]any{
			"reference": "design.md", "inventory": inventory,
		})
		for _, expression := range []string{
			`${{ path_match(inputs.reference, inputs.inventory) }}`,
			`status={{ (path_match inputs.reference inputs.inventory).status }}`,
		} {
			_, err := ctx.resolveTemplate(expression)
			require.Error(t, err)
		}
	}
}

func TestPathMatchSelectsOriginalArtifact(t *testing.T) {
	original := artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "producer", TaskOrdinal: 358, Name: "design.md", SizeBytes: 42})
	ctx := newSequenceCtx(t, newRecipeCtx(t, nil), "match", map[string]any{
		"reference": "outbox/design.md",
		"published": map[string]artifacts.Ref{"design.md": original},
	})
	result, err := ctx.resolveTemplate(`${{ inputs.published[path_match(inputs.reference, inputs.published.map(name, name)).path] }}`)
	require.NoError(t, err)
	require.Equal(t, original, result)
	result, err = ctx.resolveTemplate(`${{ path_match(inputs.reference, inputs.published.map(name, name)).status == "matched" }}`)
	require.NoError(t, err)
	require.Equal(t, true, result)
}
