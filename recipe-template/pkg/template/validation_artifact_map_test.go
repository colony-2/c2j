package template

import (
	"testing"

	recipeartifacts "github.com/colony-2/c2j/recipe-core/pkg/artifacts"
	"github.com/colony-2/c2j/recipe-core/pkg/contextual"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateMode_ArtifactLookupReturnsPlaceholderKey(t *testing.T) {
	opts := DefaultResolutionOptions()
	opts.Mode = ModeValidate
	opts.ClampSliceIndex = true

	root, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, map[string]interface{}{}, contextual.JobContext{}, opts)
	require.NoError(t, err)

	seqCtx := newSequenceCtx(t, root, "seq", map[string]interface{}{})
	seqCtx.TemplateData.Sequence["write"] = StepOutput{
		Outputs:   map[string]interface{}{},
		Artifacts: map[string]recipeartifacts.Ref{},
	}

	val, err := seqCtx.resolveTemplate(`${{ sequence.write.artifacts["foo.txt"] }}`)
	require.NoError(t, err)

	artifactRef, ok := val.(recipeartifacts.Ref)
	require.True(t, ok, "expected artifacts.Ref, got %T", val)
	key, ok := artifactRef.StoredKey()
	require.True(t, ok, "expected stored ref, got %v", artifactRef)
	assert.Equal(t, validationArtifactPlaceholderJobID, key.JobId)
	assert.Equal(t, validationArtifactPlaceholderTaskOrdinal, key.TaskOrdinal)
	assert.Equal(t, "foo.txt", key.Name)
	assert.Equal(t, int64(-1), key.SizeBytes)
}
