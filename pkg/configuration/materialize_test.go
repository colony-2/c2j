package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
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
		out, diag, err := MaterializeInput(celltest.Context(context.Background()), nil, input, fetch)
		require.NoError(t, err)
		require.Equal(t, map[string]any{"a": true, "b": []any{true, nil}}, out)
		require.Len(t, diag.Values, 2)
		require.Equal(t, "rev", diag.Revision)
	}
	require.Equal(t, 2, calls, "JobDB owns caching across invocations")
	after, _ := json.Marshal(input)
	require.Equal(t, string(before), string(after))
}
func TestMaterializeMissingProviderFailureAndForwarding(t *testing.T) {
	type forward struct {
		Inputs map[string]any `json:"inputs" secret:"forward"`
	}
	_, diag, err := MaterializeInput(celltest.Context(context.Background()), reflect.TypeFor[forward](), map[string]any{"inputs": map[string]any{"flag": Ref("flag")}}, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		t.Fatal("forwarding must not fetch")
		return jobdb.ConfigurationSnapshot{}, nil
	})
	require.NoError(t, err)
	require.Nil(t, diag)
	failure := errors.New("unavailable")
	_, _, err = MaterializeInput(celltest.Context(context.Background()), nil, Ref("flag"), func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		return jobdb.ConfigurationSnapshot{}, failure
	})
	require.ErrorIs(t, err, failure)
	out, diag, err := MaterializeInput(celltest.Context(context.Background()), nil, Ref("missing"), func(context.Context) (jobdb.ConfigurationSnapshot, error) { return jobdb.ConfigurationSnapshot{}, nil })
	require.NoError(t, err)
	require.Nil(t, out)
	require.Empty(t, diag.Values)
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
		_, _, err := MaterializeInput(celltest.Context(context.Background()), nil, ref, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
			return jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{"flag": json.RawMessage(raw)}}, nil
		})
		require.Error(t, err)
	}
}

func TestSchemaDefaultOmissionDoesNotPruneOtherNulls(t *testing.T) {
	marked := Ref("missing")
	marked.SchemaDefaultNonNullable = "input_schema.properties.optional"
	marked.ExpectedType = "string"
	input := map[string]any{"optional": marked, "ordinary": Ref("missing"), "literal": nil, "array": []any{nil, Ref("missing"), marked}, "nested": map[string]any{"literal": nil, "optional": marked}}
	raw, err := json.Marshal(input)
	require.NoError(t, err)
	var restored any
	require.NoError(t, json.Unmarshal(raw, &restored))
	for _, values := range []map[string]json.RawMessage{nil, {"missing": json.RawMessage(`null`)}} {
		out, diag, err := MaterializeInput(context.Background(), nil, restored, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
			return jobdb.ConfigurationSnapshot{Revision: "rev", Values: values}, nil
		})
		require.NoError(t, err)
		require.Equal(t, map[string]any{"ordinary": nil, "literal": nil, "array": []any{nil, nil, nil}, "nested": map[string]any{"literal": nil}}, out)
		require.Equal(t, "rev", diag.Revision)
	}
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, jobdb.ErrExecutionLeaseLost} {
		_, _, err := MaterializeInput(context.Background(), nil, restored, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
			return jobdb.ConfigurationSnapshot{}, failure
		})
		require.ErrorIs(t, err, failure)
	}
}

func TestSchemaDefaultReferenceEncodingCompatibility(t *testing.T) {
	ref := Ref("value")
	ref.Default = json.RawMessage(`null`)
	ref.ExpectedType = "string"
	ref.SchemaDefaultNonNullable = "input_schema.properties.value"
	raw, err := json.Marshal(ref)
	require.NoError(t, err)
	var value any
	require.NoError(t, json.Unmarshal(raw, &value))
	got, ok, err := Parse(value)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ref, got)
	for _, wire := range []string{
		`{"$c2j_ref":{"kind":"config","name":"value"}}`,
		`{"$c2j_ref":{"kind":"config","name":"value","default":null,"type":"string"}}`,
	} {
		require.NoError(t, json.Unmarshal([]byte(wire), &value))
		got, ok, err = Parse(value)
		require.NoError(t, err)
		require.True(t, ok)
		require.Empty(t, got.SchemaDefaultNonNullable)
	}
	for _, origin := range []any{nil, false, 42, ""} {
		_, ok, err = Parse(map[string]any{WireKey: map[string]any{"kind": "config", "name": "value", "schema_default_non_nullable": origin}})
		require.True(t, ok)
		require.Error(t, err)
	}
}
