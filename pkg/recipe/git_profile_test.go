package recipe

import (
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"testing"
)

func TestGitProfileNullSurvivesRecipeRoundTrip(t *testing.T) {
	var r Recipe
	raw := []byte("id: profiles\ngit_profile: null\nsequence:\n  - id: a\n    git_profile: work\n    sequence:\n      - id: b\n        git_profile: null\n        sequence: []\n")
	require.NoError(t, yaml.Unmarshal(raw, &r))
	require.NotNil(t, r.GetMetadata().GitProfile)
	require.Nil(t, r.GetMetadata().GitProfile.Value)
	encoded, err := yaml.Marshal(&r)
	require.NoError(t, err)
	var round Recipe
	require.NoError(t, yaml.Unmarshal(encoded, &round))
	require.NotNil(t, round.GetMetadata().GitProfile)
	a := round.RecipeImpl.(*RecipeSequence).Sequence[0].NodeImpl.(*NodeSequence)
	require.Equal(t, "work", a.GitProfile.Value)
	require.NotNil(t, a.Sequence[0].GetMetadata().GitProfile)
	require.Nil(t, a.Sequence[0].GetMetadata().GitProfile.Value)
	for _, value := range []string{"true", "[]", "{}", "''"} {
		require.Error(t, yaml.Unmarshal([]byte("id: bad\ngit_profile: "+value+"\nsequence: []\n"), &r))
	}
}
