package permissions

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRestrictionEditsGrantWithoutCreatingCeiling(t *testing.T) {
	root, err := (State{}).Enter(nil, false)
	require.NoError(t, err)
	edited, err := root.Enter(parse(t, "restrict: {jobdb: {jobs: {submit: false}}}"), false)
	require.NoError(t, err)
	require.Nil(t, edited.Grant, "Git defaults must remain deferred")
	require.Nil(t, edited.Limit, "an edit must not create a ceiling")
	for _, secret := range []string{"deployment_a", "deployment_b"} {
		g, err := edited.Resolve([]string{secret})
		require.NoError(t, err)
		require.Equal(t, []string{secret}, g.Secrets.Resolve)
		require.False(t, g.Jobs.Submit)
	}
	inherited, err := edited.Enter(parse(t, "restrict: {}"), false)
	require.NoError(t, err)
	g, err := inherited.Resolve([]string{"ro"})
	require.NoError(t, err)
	require.False(t, g.Jobs.Submit)
	for _, spec := range []string{
		"jobdb: {jobs: {submit: true}, secrets: {resolve: [x, y, z]}}",
		"jobdb: {secrets: {resolve: [z]}}",
		"jobdb: {}",
	} {
		replacement := parse(t, spec)
		child, err := edited.Enter(replacement, false)
		require.NoError(t, err)
		require.Nil(t, child.Restrict)
		g, err := child.Resolve([]string{"ro"})
		require.NoError(t, err)
		require.Equal(t, *replacement.JobDB, g)
	}
	g, err = root.Resolve([]string{"ro"})
	require.NoError(t, err)
	require.True(t, g.Jobs.Submit, "siblings recover the enclosing policy")
	require.Equal(t, []string{"perms.restrict.jobdb.jobs.submit"}, edited.Restrict.Fields())
}

func TestRestrictionsComposeWithGrantsAndLimits(t *testing.T) {
	root, err := (State{}).Enter(parse(t, "jobdb: {jobs: {submit: true, cellNames: [build]}, cells: {create: true, cellNames: [reports]}, secrets: {resolve: [x, y]}}"), false)
	require.NoError(t, err)
	child, err := root.Enter(parse(t, "restrict: {jobdb: {jobs: {submit: false}}}"), false)
	require.NoError(t, err)
	g, err := child.Resolve([]string{"ro"})
	require.NoError(t, err)
	require.False(t, g.Jobs.Submit)
	require.Nil(t, g.Jobs.CellNames)
	require.Equal(t, root.Grant.Cells, g.Cells)
	require.Equal(t, []string{"x", "y"}, g.Secrets.Resolve, "no automatic Git access in explicit grants")
	grandchild, err := child.Enter(parse(t, "restrict: {jobdb: {cells: {create: false}}}"), false)
	require.NoError(t, err)
	g, err = grandchild.Resolve(nil)
	require.NoError(t, err)
	require.False(t, g.Cells.Create)
	require.Nil(t, g.Cells.CellNames)
	require.False(t, g.Jobs.Submit)
	require.Len(t, child.Restrict.Fields(), 1, "child edits must not mutate ancestors")

	// The same-node edit is applied before validating the resulting grant.
	bounded, err := root.Enter(parse(t, "limit: {jobdb: {secrets: {resolve: [x, y]}}}\nrestrict: {jobdb: {jobs: {submit: false}, cells: {create: false}}}"), false)
	require.NoError(t, err)
	_, err = bounded.Enter(parse(t, "jobdb: {jobs: {submit: true}}"), false)
	require.ErrorContains(t, err, "exceed")
	_, err = bounded.Enter(parse(t, "jobdb: {secrets: {resolve: [z]}}"), false)
	require.ErrorContains(t, err, "exceed")

	sameNode, err := root.Enter(parse(t, "jobdb: {jobs: {submit: true}, secrets: {resolve: [z]}}\nrestrict: {jobdb: {jobs: {submit: false}}}"), false)
	require.NoError(t, err)
	g, err = sameNode.Resolve([]string{"ro"})
	require.NoError(t, err)
	require.False(t, g.Jobs.Submit)
	require.Equal(t, []string{"z"}, g.Secrets.Resolve)
	denied, err := (State{}).Enter(parse(t, "limit: {jobdb: {}}\nrestrict: {jobdb: {jobs: {submit: false}}}"), false)
	require.NoError(t, err)
	g, err = denied.Resolve([]string{"ro"})
	require.NoError(t, err)
	require.Empty(t, g.Secrets.Resolve)
	require.False(t, g.Jobs.Submit)
}

func TestRestrictionIncludeScopes(t *testing.T) {
	root, err := (State{Inputs: &InputPolicy{}}).Enter(parse(t, "restrict: {jobdb: {jobs: {submit: false}}}"), false)
	require.NoError(t, err)
	_, err = root.EnterInclude(parse(t, "restrict: {jobdb: {jobs: {submit: false}}}"), true, false)
	require.ErrorContains(t, err, "cross-repository include requires")
	inherited, err := root.EnterInclude(nil, false, true)
	require.NoError(t, err)
	g, err := inherited.Resolve([]string{"ro"})
	require.NoError(t, err)
	require.False(t, g.Jobs.Submit)
	for _, boundary := range []bool{false, true} {
		reset, err := root.EnterInclude(parse(t, "jobdb: {jobs: {submit: true}, secrets: {resolve: [ro]}}"), boundary, !boundary)
		require.NoError(t, err)
		g, err := reset.Resolve([]string{"ro"})
		require.NoError(t, err)
		require.True(t, g.Jobs.Submit)
		require.Nil(t, reset.Restrict)
	}
	foreign, err := root.EnterInclude(parse(t, "limit: {jobdb: {jobs: {submit: true}, secrets: {resolve: [ro]}}}\nrestrict: {jobdb: {jobs: {submit: false}}}"), true, false)
	require.NoError(t, err)
	g, err = foreign.Resolve([]string{"ro", "rw"})
	require.NoError(t, err)
	require.Equal(t, []string{"ro"}, g.Secrets.Resolve)
	require.False(t, g.Jobs.Submit)
	require.Error(t, foreign.CheckInput("ro"), "an edit does not authorize foreign secret inputs")
	child, err := foreign.Enter(parse(t, "jobdb: {jobs: {submit: true}}"), false)
	require.NoError(t, err)
	g, err = child.Resolve(nil)
	require.NoError(t, err)
	require.True(t, g.Jobs.Submit)
	_, err = foreign.Enter(parse(t, "jobdb: {secrets: {resolve: [rw]}}"), false)
	require.ErrorContains(t, err, "exceed")
}

func TestRestrictionPersistenceAndStrictSyntax(t *testing.T) {
	spec := parse(t, "restrict: {jobdb: {jobs: {submit: false}, cells: {create: false}}}")
	raw, err := yaml.Marshal(spec)
	require.NoError(t, err)
	require.Equal(t, spec, parse(t, string(raw)))
	s, err := (State{}).Enter(spec, false)
	require.NoError(t, err)
	raw, err = json.Marshal(s)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"submit":false`)
	var restored State
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.Equal(t, s, restored)
	g, err := restored.Resolve([]string{"new_profile_secret"})
	require.NoError(t, err)
	require.False(t, g.Jobs.Submit)
	require.Equal(t, []string{"new_profile_secret"}, g.Secrets.Resolve)
	for _, raw := range []string{
		"restrict: null", "restrict: {jobdb: null}", "restrict: {jobdb: {jobs: null}}",
		"restrict: {jobdb: {jobs: {submit: null}}}", "restrict: {jobdb: {jobs: {submit: true}}}",
		"restrict: {jobdb: {jobs: {submit: 'false'}}}", "restrict: {jobdb: {cells: {create: true}}}",
		"restrict: {jobdb: {secrets: {resolve: []}}}", "restrict: {jobdb: {jobs: {cellNames: [build]}}}",
		"restrict: {jobdb: {jobs: {submit: false, submit: false}}}", "restrict: {jobdb: {jobs: {submt: false}}}",
	} {
		var invalid Spec
		require.Error(t, yaml.Unmarshal([]byte(raw), &invalid), raw)
	}
	for _, raw := range []string{
		`{"version":1,"restrict":{"jobdb":{"jobs":{"submit":true}}}}`,
		`{"version":1,"restrict":{"jobdb":{"jobs":{"submit":null}}}}`,
		`{"version":1,"restrict":{"jobdb":{"jobs":{"submt":false}}}}`,
	} {
		require.Error(t, json.Unmarshal([]byte(raw), &restored), raw)
	}
	for _, raw := range []string{"restrict: {}", "restrict: {jobdb: {}}", "restrict: {jobdb: {jobs: {}}}"} {
		s, err := (State{}).Enter(parse(t, raw), false)
		require.NoError(t, err)
		require.Nil(t, s.Restrict)
		g, err := s.Resolve([]string{"ro"})
		require.NoError(t, err)
		require.True(t, g.Jobs.Submit)
	}
}
