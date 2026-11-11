package task

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEnvelopeExactGenericNumbersAndTypedFields(t *testing.T) {
	env := OutputEnvelope{Version: 1, Payload: json.RawMessage(`{"count":7,"values":[9007199254740993,18446744073709551617,1.23000000000000000001]}`)}
	var out struct {
		Count  int   `json:"count"`
		Values []any `json:"values"`
	}
	require.NoError(t, env.DecodePayload(&out))
	require.Equal(t, 7, out.Count)
	require.Equal(t, []any{json.Number("9007199254740993"), json.Number("18446744073709551617"), json.Number("1.23000000000000000001")}, out.Values)
	env.Payload = append(env.Payload, []byte(` {}`)...)
	require.Error(t, env.DecodePayload(&out))
}
