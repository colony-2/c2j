package permissions

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func parse(t *testing.T, raw string) *Spec {
	t.Helper()
	var spec Spec
	require.NoError(t, yaml.Unmarshal([]byte(raw), &spec))
	return &spec
}

func TestPolicyInheritanceReplacementAndCeilings(t *testing.T) {
	root, err := (State{}).Enter(nil, false)
	require.NoError(t, err)
	grant, err := root.Resolve([]string{"git-ro"})
	require.NoError(t, err)
	require.Equal(t, []string{"git-ro"}, grant.Secrets.Resolve)
	require.True(t, grant.Jobs.Submit)
	edit, err := root.Enter(parse(t, "jobdb: {secrets: {resolve: [model]}}"), false)
	require.NoError(t, err)
	grant, err = edit.Resolve([]string{"git-rw"})
	require.NoError(t, err)
	require.Equal(t, []string{"model"}, grant.Secrets.Resolve)
	require.False(t, grant.Jobs.Submit)
	denied, err := edit.Enter(parse(t, "jobdb: {}"), false)
	require.NoError(t, err)
	grant, err = denied.Resolve([]string{"git-ro"})
	require.NoError(t, err)
	require.Empty(t, grant.Secrets.Resolve)
	// Sibling policy did not change, and empty namespaces do not override it.
	sibling, err := root.Enter(parse(t, "{}"), false)
	require.NoError(t, err)
	grant, err = sibling.Resolve([]string{"git-ro"})
	require.NoError(t, err)
	require.True(t, grant.Jobs.Submit)
	foreign, err := edit.Enter(parse(t, "limit: {jobdb: {secrets: {resolve: [git-ro, model]}}}"), true)
	require.NoError(t, err)
	grant, err = foreign.Resolve([]string{"git-ro", "git-rw"})
	require.NoError(t, err)
	require.Equal(t, []string{"git-ro"}, grant.Secrets.Resolve)
	require.False(t, grant.Jobs.Submit)
	_, err = foreign.Enter(parse(t, "jobdb: {secrets: {resolve: [git-rw]}}"), false)
	require.ErrorContains(t, err, "exceed")
	_, err = foreign.Enter(parse(t, "jobdb: {jobs: {submit: true}}"), false)
	require.ErrorContains(t, err, "exceed")
	_, err = foreign.Enter(nil, true)
	require.ErrorContains(t, err, "requires perms.limit.jobdb")
	closed, err := root.Enter(parse(t, "limit: {jobdb: {}}"), false)
	require.NoError(t, err)
	_, err = closed.Enter(parse(t, "limit: {jobdb: {jobs: {submit: true}}}\njobdb: {jobs: {submit: true}}"), false)
	require.ErrorContains(t, err, "exceed")
}

func TestPermissionSyntaxRejectsIgnoredOrAmbiguousRestrictions(t *testing.T) {
	for _, input := range []string{
		"jobdb: null", "jobdb: {git: {profiles: [read]}}", "jobdb: {jobs.submit: true}",
		"jobdb: {jobs: {submit: 'false'}}", "limit: null", "limit: {jobdb: {config: {read: [x]}}}",
		"jobdb: {secrets: {resolve: [x, x]}}", "jobdb: {secrets: {resolve: ['*']}}",
		"jobdb: {secrets: {resolve: ['${{ inputs.name }}']}}", "network: {}", "jobdb: {}\njobdb: {}",
	} {
		t.Run(input, func(t *testing.T) { var spec Spec; require.Error(t, yaml.Unmarshal([]byte(input), &spec)) })
	}
	for _, raw := range []string{`{"version":99}`, `{"version":1,"grant":{"git":{}}}`, `{"version":1,"limit":{"jobs":{"submit":false}},"grant":{"jobs":{"submit":true}}}`} {
		var state State
		require.Error(t, json.Unmarshal([]byte(raw), &state))
	}
}

func TestAccessChecksBeforeCacheAndSubmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	a := NewAccess(ctx, JobDB{Secrets: SecretPermissions{Resolve: []string{"model"}}}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		calls++
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"model": {Value: "allowed"}, "rw": {Value: "cached-other-task"}}}, nil
	}, func(context.Context, jobdb.SubmitJob) (jobref.Ref, error) {
		t.Fatal("denied submit called backend")
		return jobref.Ref{}, nil
	})
	_, err := a.Resolve(ctx, []string{"rw"})
	require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
	require.Zero(t, calls)
	values, err := a.Resolve(ctx, []string{"model"})
	require.NoError(t, err)
	require.Len(t, values.Secrets, 1)
	_, err = a.Submit(ctx, jobdb.SubmitJob{})
	require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
	cancel()
	_, err = a.Resolve(context.Background(), []string{"model"})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
}
