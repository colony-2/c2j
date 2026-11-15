package recipe

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestLateIncludeBindingSyntax(t *testing.T) {
	for _, binding := range []string{`late`, `""`, `null`, `true`, `42`, `[]`, `{}`, `eager`, `early`, `'{{ inputs.bind }}'`} {
		t.Run(binding, func(t *testing.T) {
			rec, err := LoadRecipeFromString([]byte("id: parent\nsequence:\n- id: call\n  include: {bind: " + binding + ", recipe: '${{ inputs.selector }}'}\n"))
			if binding != "late" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "late", rec.RecipeImpl.(*RecipeSequence).Sequence[0].NodeImpl.(*NodeInclude).Include.Bind)
		})
	}
	for _, include := range []string{`{bind: late}`, `{bind: late, recipe: ''}`, `{bind: late, recipe: null}`, `{bind: late, recipe: 42}`, `{bind: late, recipe: cell://.c2/hooks/a.yaml}`, `{bind: late, recipe: '${{ inputs.selector }}', if_missing: {outputs: {}}}`, `{bind: late, recipe: x, unknown: x}`, `{bind: late, bind: late, recipe: x}`} {
		_, err := LoadRecipeFromString([]byte("id: parent\nsequence:\n- id: call\n  include: " + include + "\n"))
		require.Error(t, err, include)
	}
}
func TestLateIncludeOmittedBindingCompatibility(t *testing.T) {
	for _, include := range []string{`./child.yaml`, `{recipe: ./child.yaml}`, `{recipe: cell://.c2/hooks/check.yaml, if_missing: {outputs: {ok: true}}}`} {
		rec, err := LoadRecipeFromString([]byte("id: parent\nsequence:\n- id: call\n  include: " + include + "\n"))
		require.NoError(t, err)
		node := rec.RecipeImpl.(*RecipeSequence).Sequence[0].NodeImpl.(*NodeInclude)
		require.Empty(t, node.Include.Bind)
		bytes, err := yaml.Marshal(rec)
		require.NoError(t, err)
		require.False(t, strings.Contains(string(bytes), "bind:"))
	}
}
