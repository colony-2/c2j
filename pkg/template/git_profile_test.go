package template

import (
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGitProfileLexicalSelection(t *testing.T) {
	rc, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{})
	require.NoError(t, err)
	child, err := rc.NewChildContext(ScopeOp, recipe.NodeMetadata{}, "merge", nil)
	require.NoError(t, err)
	require.NoError(t, child.SelectGitProfile(nil, "merge"))
	require.True(t, child.GitProfile.Publish)
	require.False(t, rc.GitProfile.Publish)
	require.NoError(t, rc.SelectGitProfile(&recipe.GitProfileSpec{Value: "custom"}, ""))
	child, err = rc.NewChildContext(ScopeOp, recipe.NodeMetadata{}, "merge", nil)
	require.NoError(t, err)
	require.NoError(t, child.SelectGitProfile(nil, "merge"))
	require.Equal(t, "custom", child.GitProfile.Value)
	require.False(t, child.GitProfile.Publish)
	require.NoError(t, child.SelectGitProfile(&recipe.GitProfileSpec{}, "merge"))
	require.True(t, child.GitProfile.Explicit)
	require.Nil(t, child.GitProfile.Value)
	grandchild, err := child.NewChildContext(ScopeOp, recipe.NodeMetadata{}, "merge", nil)
	require.NoError(t, err)
	require.NoError(t, grandchild.SelectGitProfile(nil, "merge"))
	require.Nil(t, grandchild.GitProfile.Value)
	require.False(t, grandchild.GitProfile.Publish)
	require.NoError(t, child.SelectGitProfile(&recipe.GitProfileSpec{Value: `${{ context.config[?"git-profile-ro"].orValue(null) }}`}, ""))
	require.True(t, child.GitProfile.Explicit)
}
