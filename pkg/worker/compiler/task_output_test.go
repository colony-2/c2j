package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActivityEnvelopeV1AndV2KeepBusinessMetricFields(t *testing.T) {
	for _, raw := range []string{
		`{"v":1,"kind":"activity_output","payload":{"output":{"metrics":{"business":9007199254740993}}}}`,
		`{"v":2,"kind":"activity_output","payload":{"output":{"metrics":{"business":9007199254740993}}},"metrics":{"runtime":{"status":"absent"},"producer":{"status":"valid","report":{"version":1,"mutations":[]}}}}`,
	} {
		out, e := decodeTaskOutput([]byte(raw))
		require.NoError(t, e)
		require.Equal(t, int64(9007199254740993), out.Activity.OpOutput["metrics"].(map[string]any)["business"])
	}
	for _, raw := range []string{`{"v":99,"kind":"activity_output","payload":{}}`, `{"v":2,"kind":"unknown","payload":{}}`} {
		_, e := decodeTaskOutput([]byte(raw))
		require.Error(t, e)
	}
}
