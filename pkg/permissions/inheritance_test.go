package permissions

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestInheritedPermissionsOverrideAndReplace(t *testing.T) {
	root, err := (State{}).Enter(nil, false)
	require.NoError(t, err)
	edited, err := root.Enter(parse(t, "inherit: true\njobdb: {jobs: {submit: false}}"), false)
	require.NoError(t, err)
	require.Nil(t, edited.Grant, "Git defaults must remain deferred")
	require.Nil(t, edited.Limit, "an override must not create a ceiling")
	for _, secret := range []string{"deployment_a", "deployment_b"} {
		g, err := edited.Resolve([]string{secret})
		require.NoError(t, err)
		require.Equal(t, []string{secret}, g.Secrets.Resolve)
		require.False(t, g.Jobs.Submit)
	}
	for _, text := range []string{"{}", "inherit: true", "inherit: true\njobdb: {}", "inherit: true\njobdb: {jobs: {}}"} {
		child, err := edited.Enter(parse(t, text), false)
		require.NoError(t, err)
		g, err := child.Resolve([]string{"ro"})
		require.NoError(t, err)
		require.False(t, g.Jobs.Submit)
		require.Equal(t, []string{"ro"}, g.Secrets.Resolve)
	}
	child, err := edited.Enter(parse(t, "inherit: true\njobdb: {jobs: {submit: true}, cells: {create: true}}"), false)
	require.NoError(t, err)
	g, err := child.Resolve([]string{"ro"})
	require.NoError(t, err)
	require.True(t, g.Jobs.Submit)
	require.True(t, g.Cells.Create)
	require.Equal(t, []string{"ro"}, g.Secrets.Resolve)
	require.Empty(t, child.InheritedDenials())
	for _, spec := range []string{
		"jobdb: {jobs: {submit: true}, secrets: {resolve: [x, y, z]}}",
		"jobdb: {secrets: {resolve: [z]}}",
		"jobdb: {}", "inherit: false\njobdb: {}",
	} {
		replacement := parse(t, spec)
		child, err := edited.Enter(replacement, false)
		require.NoError(t, err)
		require.Empty(t, child.Overrides)
		g, err := child.Resolve([]string{"ro"})
		require.NoError(t, err)
		require.Equal(t, *replacement.JobDB, g)
	}
	g, err = root.Resolve([]string{"ro"})
	require.NoError(t, err)
	require.True(t, g.Jobs.Submit, "siblings recover the enclosing policy")
	require.Equal(t, []string{"perms.jobdb.jobs.submit"}, edited.InheritedDenials())
}

func TestInheritedListsReplaceAndRespectActionDependencies(t *testing.T) {
	root, err := (State{}).Enter(parse(t, "jobdb: {jobs: {submit: true, cellNames: [build]}, cells: {create: true, cellNames: [reports]}, secrets: {resolve: [x, y]}}"), false)
	require.NoError(t, err)
	child, err := root.Enter(parse(t, "inherit: true\njobdb: {jobs: {cellNames: [deploy]}, cells: {cellNames: [audit]}, secrets: {resolve: [z]}}"), false)
	require.NoError(t, err)
	g, err := child.Resolve([]string{"ro"})
	require.NoError(t, err)
	require.True(t, g.Jobs.Submit)
	require.Equal(t, []string{"deploy"}, g.Jobs.CellNames)
	require.Equal(t, []string{"audit"}, g.Cells.CellNames)
	require.Equal(t, []string{"z"}, g.Secrets.Resolve)
	empty, err := child.Enter(parse(t, "inherit: true\njobdb: {secrets: {resolve: []}, jobs: {submit: false}, cells: {create: false}}"), false)
	require.NoError(t, err)
	g, err = empty.Resolve([]string{"ro"})
	require.NoError(t, err)
	require.Empty(t, g.Secrets.Resolve)
	require.False(t, g.Jobs.Submit)
	require.Nil(t, g.Jobs.CellNames)
	require.Nil(t, g.Cells.CellNames)
	// Turning an action back on inherits the now-cleared filter, not the old list.
	enabled, err := empty.Enter(parse(t, "inherit: true\njobdb: {jobs: {submit: true}, cells: {create: true}}"), false)
	require.NoError(t, err)
	g, err = enabled.Resolve(nil)
	require.NoError(t, err)
	require.True(t, g.Jobs.Submit)
	require.Nil(t, g.Jobs.CellNames)
	require.True(t, g.Cells.Create)
	require.Nil(t, g.Cells.CellNames)
	g, err = root.Resolve(nil)
	require.NoError(t, err)
	require.Equal(t, []string{"build"}, g.Jobs.CellNames, "nested overrides must not mutate ancestors")
	_, err = empty.Enter(parse(t, "inherit: true\njobdb: {jobs: {cellNames: [build]}}"), false)
	require.ErrorContains(t, err, "requires jobs.submit")
}

func TestInheritedOverridesCannotExceedCeilings(t *testing.T) {
	for _, start := range []string{
		"limit: {jobdb: {}}",
		"jobdb: {}\nlimit: {jobdb: {}}",
	} {
		root, err := (State{}).Enter(parse(t, start), false)
		require.NoError(t, err)
		for _, override := range []string{"jobs: {submit: true}", "cells: {create: true}", "secrets: {resolve: [ro]}"} {
			_, err := root.Enter(parse(t, "inherit: true\njobdb: {"+override+"}"), false)
			require.ErrorContains(t, err, "exceed")
		}
		g, err := root.Resolve([]string{"ro"})
		require.NoError(t, err)
		require.Empty(t, g.Secrets.Resolve)
		require.False(t, g.Jobs.Submit)
	}
	root, err := (State{}).Enter(parse(t, "jobdb: {jobs: {submit: true}, secrets: {resolve: [x]}}"), false)
	require.NoError(t, err)
	bounded, err := root.Enter(parse(t, "inherit: true\njobdb: {jobs: {submit: false}}\nlimit: {jobdb: {secrets: {resolve: [x]}}}"), false)
	require.NoError(t, err)
	_, err = bounded.Enter(parse(t, "jobdb: {jobs: {submit: true}}"), false)
	require.ErrorContains(t, err, "exceed")
	limited, err := (State{}).Enter(parse(t, "limit: {jobdb: {jobs: {submit: true, cellNames: [build]}}}"), false)
	require.NoError(t, err)
	_, err = limited.Enter(parse(t, "inherit: true\njobdb: {jobs: {cellNames: [deploy]}}"), false)
	require.ErrorContains(t, err, "exceed")
	filtered, err := (State{}).Enter(parse(t, "inherit: true\njobdb: {jobs: {cellNames: [build]}}"), false)
	require.NoError(t, err)
	closed, err := filtered.Enter(parse(t, "inherit: true\njobdb: {jobs: {submit: false}}\nlimit: {jobdb: {}}"), false)
	require.NoError(t, err, "a later override can remove an inherited filtered action when tightening the ceiling")
	g, err := closed.Resolve(nil)
	require.NoError(t, err)
	require.False(t, g.Jobs.Submit)
	require.Nil(t, g.Jobs.CellNames)
}

func TestInheritanceIncludeScopesAndInputAuthority(t *testing.T) {
	root, err := (State{Inputs: &InputPolicy{}}).Enter(parse(t, "inherit: true\njobdb: {jobs: {submit: false}}"), false)
	require.NoError(t, err)
	_, err = root.EnterInclude(parse(t, "inherit: true\njobdb: {jobs: {submit: false}}"), true, false)
	require.ErrorContains(t, err, "cross-repository include requires")
	child, err := root.EnterInclude(parse(t, "inherit: true\njobdb: {jobs: {submit: true}}"), false, true)
	require.NoError(t, err)
	g, err := child.Resolve([]string{"ro"})
	require.NoError(t, err)
	require.True(t, g.Jobs.Submit)
	require.Equal(t, []string{"ro"}, g.Secrets.Resolve)
	require.Nil(t, child.Limit)
	for _, boundary := range []bool{false, true} {
		reset, err := root.EnterInclude(parse(t, "jobdb: {jobs: {submit: true}, secrets: {resolve: [ro]}}"), boundary, !boundary)
		require.NoError(t, err)
		g, err := reset.Resolve([]string{"ro"})
		require.NoError(t, err)
		require.True(t, g.Jobs.Submit)
		require.Empty(t, reset.Overrides)
	}
	foreign, err := root.EnterInclude(parse(t, "limit: {jobdb: {jobs: {submit: true}, secrets: {resolve: [ro, x]}}}\ninherit: true\njobdb: {jobs: {submit: false}}"), true, false)
	require.NoError(t, err)
	g, err = foreign.Resolve([]string{"ro", "rw"})
	require.NoError(t, err)
	require.Equal(t, []string{"ro"}, g.Secrets.Resolve)
	require.Error(t, foreign.CheckInput("ro"), "automatic Git credentials do not authorize foreign inputs")
	withSecret, err := foreign.Enter(parse(t, "inherit: true\njobdb: {secrets: {resolve: [x]}}"), false)
	require.NoError(t, err)
	require.NoError(t, withSecret.CheckInput("x"))
	cleared, err := withSecret.Enter(parse(t, "inherit: true\njobdb: {secrets: {resolve: []}}"), false)
	require.NoError(t, err)
	require.Error(t, cleared.CheckInput("x"))
	_, err = foreign.Enter(parse(t, "inherit: true\njobdb: {secrets: {resolve: [rw]}}"), false)
	require.ErrorContains(t, err, "exceed")
}

func TestInheritancePersistenceAndStrictSyntax(t *testing.T) {
	for _, text := range []string{
		"inherit: true\njobdb: {jobs: {submit: false}, cells: {create: true}}",
		"inherit: true\njobdb: {secrets: {resolve: []}}",
		"inherit: true\njobdb: {}", "inherit: true", "jobdb: {}",
	} {
		spec := parse(t, text)
		raw, err := yaml.Marshal(spec)
		require.NoError(t, err)
		require.Equal(t, spec, parse(t, string(raw)))
		raw, err = json.Marshal(spec)
		require.NoError(t, err)
		var copy Spec
		require.NoError(t, json.Unmarshal(raw, &copy))
		require.Equal(t, spec, &copy)
		s, err := (State{}).Enter(spec, false)
		require.NoError(t, err)
		raw, err = json.Marshal(s)
		require.NoError(t, err)
		var restored State
		require.NoError(t, json.Unmarshal(raw, &restored))
		require.Equal(t, s, restored)
		want, err := s.Resolve([]string{"new_profile_secret"})
		require.NoError(t, err)
		got, err := restored.Resolve([]string{"new_profile_secret"})
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	for _, text := range []string{
		"inherit: null", "inherit: 'true'", "inherit: true\ninherit: false", "restrict: {}",
		"inherit: true\njobdb: null", "inherit: true\njobdb: {jobs: {submit: null}}",
		"inherit: true\njobdb: {secrets: {resolve: null}}", "inherit: true\njobdb: {secrets: {resolve: [x, x]}}",
		"inherit: true\njobdb: {jobs: {submit: false, cellNames: [build]}}",
		"inherit: true\njobdb: {jobs: {cellNames: []}}", "inherit: true\njobdb: {cells: {cellNames: []}}",
		"inherit: true\njobdb: {jobs: {submit: false, submit: true}}", "inherit: true\njobdb: {jobs: {submt: false}}",
	} {
		var invalid Spec
		require.Error(t, yaml.Unmarshal([]byte(text), &invalid), text)
	}
	for _, raw := range []string{
		`{"inherit":null}`, `{"inherit":true,"jobdb":{"jobs":{"submit":null}}}`,
		`{"inherit":true,"jobdb":{"secrets":{"resolve":null}}}`, `{"restrict":{}}`,
	} {
		var invalid Spec
		require.Error(t, json.Unmarshal([]byte(raw), &invalid), raw)
	}
	for _, raw := range []string{
		`{"version":1,"overrides":[{"jobs":{"submit":null}}]}`,
		`{"version":1,"overrides":[{"secrets":{"resolve":null}}]}`,
		`{"version":1,"restrict":{}}`,
	} {
		var invalid State
		require.Error(t, json.Unmarshal([]byte(raw), &invalid), raw)
	}
}
