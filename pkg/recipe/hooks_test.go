package recipe

import (
	"github.com/stretchr/testify/require"
	"testing"
)

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
