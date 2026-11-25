package recipe

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestParallelRecipeSyntax(t *testing.T) {
	valid := []string{
		"id: root\nparallel: {branches: [{id: one, sequence: []}]}\n",
		"id: root\nsequence: [{id: check, parallel: {mode: wait_all, branches: [{id: one, sequence: []}]}}]\n",
		"id: root\ndefs: {check: {id: one, sequence: []}}\nparallel: {branches: [{shared: check}]}\n",
	}
	for _, source := range valid {
		require.NoError(t, Validate(source))
		loaded, err := LoadRecipeFromString([]byte(source))
		require.NoError(t, err)
		encoded, err := yaml.Marshal(loaded)
		require.NoError(t, err)
		_, err = LoadInternalRecipeFromString(encoded)
		require.NoError(t, err)
	}
	for _, invalid := range []struct{ yaml, error string }{
		{"parallel: {mode: fail_fast, branches: []}", "unsupported"},
		{"parallel: {mode: '', branches: []}", "unsupported"},
		{"parallel: null", "must be a mapping"},
		{"sequence: []\nparallel: {mode: fail_fast, branches: []}", "cannot be combined"},
		{"parallel: {branches: [{sequence: []}]}", "nonempty id"},
		{"parallel: {branches: [{id: one, sequence: []}, {id: one, sequence: []}]}", "duplicate parallel branch id"},
		{"parallel: {branches: [{id: one, sequence: [], __c2j_internal: {root_repository: foreign}}]}", "reserved"},
	} {
		_, err := LoadRecipeFromString([]byte(invalid.yaml))
		require.ErrorContains(t, err, invalid.error)
	}
}
