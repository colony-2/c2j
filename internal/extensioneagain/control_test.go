package extensioneagain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtensionEAGAINDecode(t *testing.T) {
	for _, raw := range []string{
		`{"eagain":{"version":1,"mode":"abandon"}}`,
		`{"eagain":{"version":1,"mode":"persist","next_inputs":{}}}`,
		`{"eagain":{"version":1,"mode":"persist","next_inputs":{"key":null,"ordinal":9007199254740993}}}`,
	} {
		request, err := Decode([]byte(raw), len(raw))
		require.NoError(t, err)
		require.True(t, request.Valid())
		_, err = Decode([]byte(raw), len(raw)-1)
		require.Error(t, err)
	}
	for _, raw := range []string{
		`{"eagain":{"version":1,"mode":"persist"}}`,
		`{"eagain":{"version":1,"mode":"persist","next_inputs":null}}`,
		`{"eagain":{"version":1,"mode":"abandon","next_inputs":null}}`,
		`{"eagain":{"version":1,"mode":"abandon","next_inputs":{}}}`,
		`{"EAGAIN":{"version":1,"mode":"abandon"}}`,
		`{"eagain":{"Version":1,"mode":"abandon"}}`,
		`{"eagain":null}`,
		`{"eagain":{"version":2,"mode":"abandon"}}`,
		`{"eagain":{"version":1,"mode":"wait"}}`,
		`{"eagain":{"version":1,"mode":"abandon","mode":"abandon"}}`,
		`{"eagain":{"version":1,"mode":"persist","next_inputs":{"key":{"nested":1,"nested":2}}}}`,
		`{"eagain":{"version":1,"mode":"abandon"},"output":{}}`,
		`{"eagain":{"version":1,"mode":"abandon"}} {}`,
	} {
		_, err := Decode([]byte(raw), len(raw))
		require.Error(t, err, "%s", raw)
	}
}
