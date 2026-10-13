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

func TestNullFallbackReferenceRoundTrip(t *testing.T) {
	ref, ok := Expression("context.secrets.?token.orValue(null)")
	require.True(t, ok)
	require.True(t, ref.OmitIfUnavailable)
	require.Equal(t, ref, WithDefault(ref, "schema"), "explicit null wins over schema default")
	for _, required := range []bool{false, true} {
		ref.Required = required
		raw, err := json.Marshal(ref)
		require.NoError(t, err)
		var wire any
		require.NoError(t, json.Unmarshal(raw, &wire))
		decoded, ok, err := Parse(wire)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, ref, decoded)
	}
	for _, invalid := range []any{
		map[string]any{WireKey: "token", "omit_if_unavailable": true, "default": ""},
		map[string]any{WireKey: "token", "omit_if_unavailable": "true"},
		map[string]any{WireKey: "token", "required": true},
	} {
		_, _, err := Parse(invalid)
		require.Error(t, err)
	}
}

func TestNullFallbackMaterialization(t *testing.T) {
	s := NewSession()
	defer s.Close()
	ref := Reference{Name: "missing", OmitIfUnavailable: true}
	refs := []Reference{ref, {Name: "empty", OmitIfUnavailable: true}, {Name: "present", OmitIfUnavailable: true}}
	require.NoError(t, s.PrepareReferences(context.Background(), refs, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"empty": {Value: ""}, "present": {Value: "private"}}}, nil
	}))
	input := map[string]any{
		"missing": ref, "empty": refs[1], "present": refs[2],
		"nested": map[string]any{"missing": ref, "null": nil},
		"list":   []any{map[string]any{"missing": ref}},
	}
	got, err := s.Materialize(input)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"empty": "", "present": "private", "nested": map[string]any{"null": nil}, "list": []any{map[string]any{}},
	}, got)
	require.Contains(t, input, "missing", "durable input must stay unchanged")
	require.False(t, s.Ready([]string{"missing"}))
	_, err = s.Materialize([]any{ref})
	require.ErrorContains(t, err, "array element")
	type native struct {
		Token *string           `json:"token" secret:"true"`
		Env   map[string]string `json:"env" secret:"env"`
		Child map[string]any    `json:"child" secret:"forward"`
	}
	actual, unset, err := MaterializeInputWithUnsetEnv(s, reflect.TypeFor[native](), map[string]any{
		"token": ref, "env": map[string]any{"TOKEN": ref, "EMPTY": refs[1]},
		"child": map[string]any{"token": ref},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"TOKEN"}, unset)
	require.Equal(t, map[string]any{"env": map[string]any{"EMPTY": ""}, "child": map[string]any{"token": ref}}, actual)
	// A required recipe binding must remain required when forwarded.
	ref.Required = true
	err = s.PrepareReferences(context.Background(), []Reference{ref}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{}, nil
	})
	require.ErrorContains(t, err, "required secret unavailable")
}

func TestNullFallbackDoesNotHideProviderErrors(t *testing.T) {
	for _, providerErr := range []error{errors.New("outage"), jobdb.ErrNoSecretProvider, context.Canceled, context.DeadlineExceeded, jobdb.ErrExecutionLeaseLost} {
		s := NewSession()
		err := s.PrepareReferences(context.Background(), []Reference{{Name: "missing", OmitIfUnavailable: true}},
			func(context.Context, []string) (jobdb.SecretResolveResult, error) {
				return jobdb.SecretResolveResult{}, providerErr
			})
		require.Error(t, err)
		s.Close()
	}
}
