package permissions

import (
	"context"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestInputResolverChecksNamesAndTaskLifetime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	access := NewAccess(ctx, JobDB{Secrets: SecretPermissions{Resolve: []string{"bar"}}}, nil, nil)
	calls := 0
	ctx = WithInputResolver(WithAccess(ctx, access), func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		calls++
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"foo": {Value: "input"}, "bar": {Value: "unrequested"}}}, nil
	})
	values, err := ResolveInputs(ctx, []string{"foo"}, []string{"foo"})
	require.NoError(t, err)
	require.Equal(t, map[string]jobdb.SecretValue{"foo": {Value: "input"}}, values.Secrets)
	_, err = ResolveInputs(ctx, []string{"bar"}, []string{"foo"})
	require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
	require.ErrorIs(t, access.CheckSecrets([]string{"foo"}), jobdb.ErrLeasePermissionDenied)
	cancel()
	_, err = ResolveInputs(context.WithoutCancel(ctx), []string{"foo"}, []string{"foo"})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
}

func TestInputAuthorityIsIndependentOfLeaseGrant(t *testing.T) {
	root := State{Version: Version, Inputs: &InputPolicy{RootRepository: "https://example.test/root"}}
	bar, err := root.Enter(parse(t, "jobdb: {secrets: {resolve: [bar]}}"), false)
	require.NoError(t, err)
	require.NoError(t, bar.CheckInput("foo"))
	grant, err := bar.Resolve(nil)
	require.NoError(t, err)
	require.Equal(t, []string{"bar"}, grant.Secrets.Resolve)
	deny, err := root.Enter(parse(t, "jobdb: {}"), false)
	require.NoError(t, err)
	require.NoError(t, deny.CheckInput("foo"))
	grant, err = deny.Resolve(nil)
	require.NoError(t, err)
	require.Empty(t, grant.Secrets.Resolve)

	foreign, err := root.EnterInclude(parse(t, "jobdb: {secrets: {resolve: [foo, bar]}}"), true, false)
	require.NoError(t, err)
	require.NoError(t, foreign.CheckInput("foo"))
	require.ErrorIs(t, foreign.CheckInput("other"), jobdb.ErrLeasePermissionDenied)
	grant, err = foreign.Resolve(nil)
	require.NoError(t, err)
	require.Empty(t, grant.Secrets.Resolve, "include grants must not become ambient lease grants")
	child, err := foreign.Enter(parse(t, "jobdb: {secrets: {resolve: [bar]}}"), false)
	require.NoError(t, err)
	require.NoError(t, child.CheckInput("foo"), "node grants must not replace include input authority")
	grant, err = child.Resolve(nil)
	require.NoError(t, err)
	require.Equal(t, []string{"bar"}, grant.Secrets.Resolve)
	_, err = foreign.Enter(parse(t, "jobdb: {secrets: {resolve: [other]}}"), false)
	require.Error(t, err)
	_, err = foreign.EnterInclude(parse(t, "jobdb: {secrets: {resolve: [other]}}"), true, false)
	require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
	// A same-repository include inside a foreign recipe remains foreign to root.
	nested, err := foreign.EnterInclude(nil, false, false)
	require.NoError(t, err)
	require.True(t, nested.Inputs.Foreign)
	require.Equal(t, root.Inputs.RootRepository, nested.Inputs.RootRepository)
	require.NoError(t, nested.CheckInput("foo"))
	require.Error(t, nested.CheckInput("other"))
}
