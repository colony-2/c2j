package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestMaterializeUsesTaskAPIAndPreservesInput(t *testing.T) {
	input := map[string]any{"a": Ref("flag"), "b": []any{Ref("flag"), Ref("null")}}
	before, _ := json.Marshal(input)
	calls := 0
	fetch := func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		calls++
		return jobdb.ConfigurationSnapshot{Revision: "rev", Values: map[string]json.RawMessage{"flag": json.RawMessage(`true`), "null": json.RawMessage(`null`), "unused": json.RawMessage(`42`)}}, nil
	}
	for range 2 {
		out, diag, err := MaterializeInput(context.Background(), nil, input, fetch)
		require.NoError(t, err)
		require.Equal(t, map[string]any{"a": true, "b": []any{true, nil}}, out)
		require.Len(t, diag.Values, 2)
		require.Equal(t, "rev", diag.Revision)
	}
	require.Equal(t, 2, calls, "JobDB owns caching across invocations")
	after, _ := json.Marshal(input)
	require.Equal(t, string(before), string(after))
}
func TestMaterializeMissingFailureAndForwarding(t *testing.T) {
	type forward struct {
		Inputs map[string]any `json:"inputs" secret:"forward"`
	}
	_, diag, err := MaterializeInput(context.Background(), reflect.TypeFor[forward](), map[string]any{"inputs": map[string]any{"flag": Ref("flag")}}, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		t.Fatal("forwarding must not fetch")
		return jobdb.ConfigurationSnapshot{}, nil
	})
	require.NoError(t, err)
	require.Nil(t, diag)
	failure := errors.New("unavailable")
	_, _, err = MaterializeInput(context.Background(), nil, Ref("flag"), func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		return jobdb.ConfigurationSnapshot{}, failure
	})
	require.ErrorIs(t, err, failure)
	_, diag, err = MaterializeInput(context.Background(), nil, Ref("missing"), func(context.Context) (jobdb.ConfigurationSnapshot, error) { return jobdb.ConfigurationSnapshot{}, nil })
	require.ErrorContains(t, err, "missing")
	require.Equal(t, []string{"missing"}, diag.Missing)
}
func TestReferenceRoundTripAndValidation(t *testing.T) {
	ref := Ref("flag")
	ref.ExpectedType = "boolean"
	raw, err := json.Marshal(ref)
	require.NoError(t, err)
	var value any
	require.NoError(t, json.Unmarshal(raw, &value))
	got, ok, err := Parse(value)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ref, got)
	for _, raw := range []string{`{"$c2j_ref":null}`, `{"$c2j_ref":{"kind":"secret","name":"x"}}`, `{"$c2j_ref":{"kind":"config","name":"x","extra":true}}`} {
		require.NoError(t, json.Unmarshal([]byte(raw), &value))
		_, ok, err := Parse(value)
		require.True(t, ok)
		require.Error(t, err)
	}
	for _, raw := range []string{`"wrong type"`, `{"$c2j_secret":"password"}`, `{"$c2j_ref":{"kind":"config","name":"other"}}`} {
		_, _, err := MaterializeInput(context.Background(), nil, ref, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
			return jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{"flag": json.RawMessage(raw)}}, nil
		})
		require.Error(t, err)
	}
}
