package recipe

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRecipePermissionsRoundTripAndStrictFields(t *testing.T) {
	for _, shape := range []string{"sequence: []", "state: {initial: end, states: {end: {sequence: []}}}", "child_group: {mode: run, children: []}"} {
		rec, err := LoadRecipeFromString([]byte("id: scoped\nperms: {jobdb: {cells: {create: true, cellNames: [reports]}}, limit: {jobdb: {cells: {create: true, cellNames: [reports]}}}}\n" + shape))
		require.NoError(t, err)
		require.NotNil(t, rec.GetMetdata().Perms)
		raw, err := yaml.Marshal(rec)
		require.NoError(t, err)
		copy, err := LoadRecipeFromString(raw)
		require.NoError(t, err)
		require.NotNil(t, copy.GetMetdata().Perms.JobDB)
		require.NotNil(t, copy.GetMetdata().Perms.Limit.JobDB)
		require.True(t, copy.GetMetdata().Perms.JobDB.Cells.Create)
		require.Equal(t, []string{"reports"}, copy.GetMetdata().Perms.Limit.JobDB.Cells.CellNames)
	}
	for _, spec := range []string{"null", "{jobdb: null}", "{jobdb: {git: {profiles: [read]}}}", "{limit: {jobdb: null}}"} {
		_, err := LoadRecipeFromString([]byte("id: invalid\nsequence: []\nperms: " + spec))
		require.Error(t, err)
	}
	_, err := LoadRecipeFromString([]byte("id: invalid\ndefs: {x: {sequence: []}}\nsequence: [{shared: x, perms: {}}]"))
	require.ErrorContains(t, err, "shared definition")
	plain, err := LoadRecipeFromString([]byte("id: plain\nsequence: []"))
	require.NoError(t, err)
	require.Nil(t, plain.GetMetdata().Perms)
	// Empty perms inherits; explicit empty jobdb denies instead of disappearing.
	rec, err := LoadRecipeFromReader(strings.NewReader("id: enabled\nperms: {}\nsequence: []"))
	require.NoError(t, err)
	require.NotNil(t, rec.GetMetdata().Perms)
}

func TestRecipeInheritanceRoundTrip(t *testing.T) {
	registerTestOp()
	for _, shape := range []string{"op: echo\ninputs: {message: hi}", "sequence: []", "state: {initial: end, states: {end: {sequence: []}}}", "child_group: {mode: run, children: []}"} {
		rec, err := LoadRecipeFromString([]byte("id: restricted\nperms: {inherit: true, jobdb: {jobs: {submit: false}}}\n" + shape))
		require.NoError(t, err)
		raw, err := yaml.Marshal(rec)
		require.NoError(t, err)
		copy, err := LoadRecipeFromString(raw)
		require.NoError(t, err)
		require.NotNil(t, copy.GetMetdata().Perms.Overrides.Jobs.Submit)
		require.False(t, *copy.GetMetdata().Perms.Overrides.Jobs.Submit)
	}
	require.NoError(t, Validate("id: restricted\nop: echo\ninputs: {message: hi}\nperms: {inherit: true, jobdb: {jobs: {submit: false}}}"))
	require.NoError(t, Validate("id: inherited\nop: echo\ninputs: {message: hi}\nperms: {inherit: true, jobdb: {jobs: {submit: true}}}"))
}
