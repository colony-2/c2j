package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestFallbackBindingsAndRefresh(t *testing.T) {
	session := NewSession()
	defer session.Close()
	refs := []Reference{
		WithDefault(Reference{Name: "missing"}, "first"),
		WithDefault(Reference{Name: "missing"}, "second"),
		WithDefault(Reference{Name: "empty"}, "fallback"),
		WithDefault(Reference{Name: "present"}, "fallback"),
	}
	calls := 0
	resolve := func(_ context.Context, names []string) (jobdb.SecretResolveResult, error) {
		calls++
		require.ElementsMatch(t, []string{"missing", "empty", "present"}, names)
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{
			"empty": {Value: ""}, "present": {Value: "private"},
		}}, nil
	}
	require.NoError(t, session.PrepareReferences(context.Background(), refs, resolve))
	for i, expected := range []string{"first", "second", "", "private"} {
		got, err := session.Materialize(refs[i])
		require.NoError(t, err)
		require.Equal(t, expected, got)
	}
	require.Equal(t, 1, calls)
	require.False(t, session.Ready([]string{"missing"}))
	require.True(t, session.Ready([]string{"present", "empty"}))
	// A fallback is public, so it is neither masked nor stored as a secret.
	require.Equal(t, "first second [REDACTED]", session.Redact("first second private"))
	// Every preparation asks the resolver again; JobDB determines reuse.
	require.NoError(t, session.PrepareReferences(context.Background(), refs, func(_ context.Context, names []string) (jobdb.SecretResolveResult, error) {
		require.ElementsMatch(t, []string{"missing", "empty", "present"}, names)
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"missing": {Value: "new-private"}}}, nil
	}))
	got, err := session.Materialize(refs[0])
	require.NoError(t, err)
	require.Equal(t, "new-private", got)
}

func TestFallbackNeverHidesRequiredBindingsOrProviderErrors(t *testing.T) {
	optional := WithDefault(Reference{Name: "missing"}, "")
	for _, tc := range []struct {
		name        string
		refs        []Reference
		providerErr error
		want        string
	}{
		{"required", []Reference{optional, {Name: "missing"}}, nil, "required secret unavailable"},
		{"outage", []Reference{optional}, errors.New("private provider detail"), "secret resolution unavailable"},
		{"no provider", []Reference{optional}, jobdb.ErrNoSecretProvider, "secret resolution unavailable"},
		{"canceled", []Reference{optional}, context.Canceled, "secret resolution unavailable"},
		{"deadline", []Reference{optional}, context.DeadlineExceeded, "secret resolution unavailable"},
		{"lease", []Reference{optional}, jobdb.ErrExecutionLeaseLost, jobdb.ErrExecutionLeaseLost.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := NewSession()
			defer session.Close()
			err := session.PrepareReferences(context.Background(), tc.refs, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
				return jobdb.SecretResolveResult{}, tc.providerErr
			})
			require.ErrorContains(t, err, tc.want)
			require.NotContains(t, err.Error(), "private provider detail")
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := NewSession().PrepareReferences(ctx, []Reference{optional}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		t.Fatal("canceled setup must not fetch")
		return jobdb.SecretResolveResult{}, nil
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestFallbackReferencesRoundTripAndForwarding(t *testing.T) {
	ref := WithDefault(Reference{Name: "missing"}, "")
	raw, err := json.Marshal(ref)
	require.NoError(t, err)
	var value any
	require.NoError(t, json.Unmarshal(raw, &value))
	decoded, ok, err := Parse(value)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ref, decoded)
	for _, invalid := range []any{
		map[string]any{WireKey: "token", "default": nil},
		map[string]any{WireKey: "token", "default": 123},
		map[string]any{WireKey: "token", "other": ""},
		map[string]any{WireKey: ""},
	} {
		_, _, err := Parse(invalid)
		require.Error(t, err)
	}
	type input struct {
		Token string         `json:"token" secret:"true"`
		Child map[string]any `json:"child" secret:"forward"`
	}
	in := map[string]any{"token": ref, "child": map[string]any{"token": Reference{Name: "child"}}}
	refs, err := InvocationReferences(reflect.TypeFor[input](), in)
	require.NoError(t, err)
	require.Equal(t, []Reference{ref}, refs)
	defaultOnly := WithDefault(Reference{}, "")
	require.True(t, Contains(defaultOnly))
	names, err := Names(defaultOnly)
	require.NoError(t, err)
	require.Empty(t, names)
	require.Error(t, NewSession().CheckOutput(defaultOnly))
}

func TestNestedSchemaFallbacks(t *testing.T) {
	leaf := map[string]any{"type": "string", "x-secret": true, "default": "public"}
	schema := map[string]any{"properties": map[string]any{
		"nested": map[string]any{"properties": map[string]any{"token": leaf}},
		"list":   map[string]any{"items": leaf},
		"env":    map[string]any{"additionalProperties": leaf},
	}}
	input := map[string]any{
		"nested": map[string]any{},
		"list":   []any{Reference{Name: "missing"}},
		"env":    map[string]any{"TOKEN": WithDefault(Reference{Name: "missing"}, "explicit")},
	}
	output, err := ApplySchemaDefaults(schema, input)
	require.NoError(t, err)
	require.NoError(t, ValidateSchema(schema, output, false))
	actual, err := NewSession().Materialize(output)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"nested": map[string]any{"token": "public"},
		"list":   []any{"public"},
		"env":    map[string]any{"TOKEN": "explicit"},
	}, actual)
	require.Empty(t, input["nested"], "defaults must not mutate callers' input")
}
