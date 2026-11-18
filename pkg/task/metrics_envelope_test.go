package task

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/stretchr/testify/require"
)

func TestInternalEnvelopePreservesLegacyPayloadAndReports(t *testing.T) {
	payload := map[string]any{"workspace": "same", "count": int64(9007199254740993)}
	raw, e := json.Marshal(payload)
	require.NoError(t, e)
	var old map[string]any
	require.NoError(t, DecodeInternal(raw, &old))
	envelope, e := NewOutputEnvelope(OutputKindInternalResult, payload)
	require.NoError(t, e)
	envelope.Metrics = &metrics.Channels{Runtime: metrics.Channel{Status: "valid", Report: &metrics.Report{Version: 1, Mutations: []metrics.Mutation{}}}, Producer: metrics.Channel{Status: "absent"}}
	raw, e = json.Marshal(envelope)
	require.NoError(t, e)
	var current map[string]any
	require.NoError(t, DecodeInternal(raw, &current))
	require.Equal(t, old, current)
	var decoded OutputEnvelope
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.NotNil(t, decoded.Metrics)
	for _, invalid := range []string{`{"v":99,"kind":"internal_result","payload":{}}`, `{"v":2,"kind":"activity_output","payload":{}}`} {
		require.Error(t, DecodeInternal([]byte(invalid), &current))
	}
}
