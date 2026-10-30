package recipe

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestHookSourceConfiguration(t *testing.T) {
	for _, mode := range []string{"from_base", "from_base_with_rebase", "from_current"} {
		body := fmt.Sprintf("id: parent\nsource_hooks: %s\nsequence: []\n", mode)
		rec, err := LoadRecipeFromString([]byte(body))
		require.NoError(t, err)
		require.NoError(t, Validate(body))
		require.Equal(t, HookSource(mode), rec.GetMetadata().SourceHooks)
		raw, err := yaml.Marshal(rec)
		require.NoError(t, err)
		round, err := LoadInternalRecipeFromString(raw)
		require.NoError(t, err)
		require.Equal(t, rec.GetMetadata().SourceHooks, round.GetMetadata().SourceHooks)
	}
	for _, mode := range []string{"from_main", "typo", "[from_base]", "42"} {
		body := fmt.Sprintf("id: parent\nsource_hooks: %s\nsequence: []\n", mode)
		_, err := LoadRecipeFromString([]byte(body))
		require.Error(t, err)
		require.Error(t, Validate(body))
	}
}

func TestHookAuthoringSchemaAndFallbackParser(t *testing.T) {
	for _, inc := range []string{"cell://.c2/hooks/check.yaml", "{recipe: 'cell://.c2/hooks/check.yaml', if_missing: {outputs: {decision: continue}}}"} {
		body := "id: parent\nsequence:\n - id: hook\n   include: " + inc + "\n   expect_outputs: {type: object, required: [decision]}\n"
		_, err := LoadRecipeFromString([]byte(body))
		require.NoError(t, err)
		require.NoError(t, Validate(body))
	}
	for _, fallback := range []string{"null", "{outputs: null}", "{outputs: []}", "{typo: {}}", "{outputs: {}, typo: true}"} {
		body := "id: parent\nsequence:\n - include: {recipe: 'cell://.c2/hooks/check.yaml', if_missing: " + fallback + "}\n"
		_, err := LoadRecipeFromString([]byte(body))
		require.Error(t, err, fallback)
		require.Error(t, Validate(body), fallback)
	}
}

func TestHookAuthorityCannotBeSuppliedByRecipeArtifact(t *testing.T) {
	rec, err := LoadInternalRecipeFromString([]byte(`id: parent
sequence:
 - id: forged
   sequence: []
   __c2j_internal:
     inline: {recipe_id: forged}
     hook_authority:
       version: 1
       grant: {secrets: {resolve: [stolen]}, jobs: {submit: true}}
       limit: {secrets: {resolve: [stolen]}, jobs: {submit: true}}
`))
	require.NoError(t, err)
	node := rec.RecipeImpl.(*RecipeSequence).Sequence[0]
	require.Nil(t, node.GetMetadata().Internal.HookAuthority)
}
