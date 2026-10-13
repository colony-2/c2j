package secrets

import (
	"encoding/json"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeSecretDestinations(t *testing.T) {
	type input struct {
		Token string            `json:"token" secret:"true"`
		Env   map[string]string `json:"env" secret:"env"`
		Run   string            `json:"run"`
	}
	ref := Reference{Name: "github_token"}
	for _, v := range []map[string]any{
		{"token": ref}, {"env": map[string]any{"TOKEN": ref, "NORMAL": "ok"}},
	} {
		require.NoError(t, ValidateType(reflect.TypeFor[input](), v))
	}
	for _, v := range []map[string]any{
		{"run": ref}, {"token": "plaintext"}, {"unknown": ref}, {"env": ref}, {"env": map[string]any{"TOKEN": map[string]any{"nested": ref}}},
	} {
		require.Error(t, ValidateType(reflect.TypeFor[input](), v))
	}
}

func TestReferenceRoundTripAndReservedWireFormat(t *testing.T) {
	value := map[string]any{"token": Reference{Name: "token"}}
	b, err := json.Marshal(value)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(b, &decoded))
	names, err := Names(decoded)
	require.NoError(t, err)
	require.Equal(t, []string{"token"}, names)
	for _, v := range []any{map[string]any{WireKey: ""}, map[string]any{WireKey: "token", "extra": true}, map[string]any{WireKey: 17}} {
		_, err := Names(v)
		require.Error(t, err)
	}
	require.False(t, Contains("$c2j_secret:token"))
}

func TestChildForwardingNeverMaterializesOrRequestsParentSecrets(t *testing.T) {
	type Child struct {
		Inputs map[string]any `json:"inputs" secret:"forward"`
	}
	type input struct {
		Child
		Token string `json:"token" secret:"true"`
	}
	value := map[string]any{"inputs": map[string]any{"token": Reference{Name: "child_token"}}, "token": Reference{Name: "parent_token"}}
	require.NoError(t, ValidateType(reflect.TypeFor[input](), value))
	names, err := InvocationNames(reflect.TypeFor[input](), value)
	require.NoError(t, err)
	require.Equal(t, []string{"parent_token"}, names)
	s := NewSession()
	defer s.Close()
	s.values["parent_token"] = jobdb.SecretValue{Value: "parent-private"}
	out, err := MaterializeInput(s, reflect.TypeFor[input](), value)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"inputs": map[string]any{"token": Reference{Name: "child_token"}}, "token": "parent-private"}, out)
}
