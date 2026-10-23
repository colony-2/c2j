package recipe

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRecipePermissionsRoundTripAndStrictFields(t *testing.T) {
	for _, shape := range []string{"sequence: []", "state: {initial: end, states: {end: {sequence: []}}}", "child_group: {mode: run, children: []}"} {
		rec, err := LoadRecipeFromString([]byte("id: scoped\nperms: {jobdb: {}, limit: {jobdb: {jobs: {submit: false}}}}\n" + shape))
		require.NoError(t, err)
		require.True(t, HasPermissions(*rec))
		raw, err := yaml.Marshal(rec)
		require.NoError(t, err)
		copy, err := LoadRecipeFromString(raw)
		require.NoError(t, err)
		require.NotNil(t, copy.GetMetdata().Perms.JobDB)
		require.NotNil(t, copy.GetMetdata().Perms.Limit.JobDB)
	}
	for _, spec := range []string{"null", "{jobdb: null}", "{jobdb: {git: {profiles: [read]}}}", "{limit: {jobdb: null}}"} {
		_, err := LoadRecipeFromString([]byte("id: invalid\nsequence: []\nperms: " + spec))
		require.Error(t, err)
	}
	_, err := LoadRecipeFromString([]byte("id: invalid\ndefs: {x: {sequence: []}}\nsequence: [{shared: x, perms: {}}]"))
	require.ErrorContains(t, err, "shared definition")
	legacy, err := LoadRecipeFromString([]byte("id: old\nsequence: []"))
	require.NoError(t, err)
	require.False(t, HasPermissions(*legacy))
	// Empty perms opts in; explicit empty jobdb denies instead of disappearing.
	rec, err := LoadRecipeFromReader(strings.NewReader("id: enabled\nperms: {}\nsequence: []"))
	require.NoError(t, err)
	require.True(t, HasPermissions(*rec))
}
