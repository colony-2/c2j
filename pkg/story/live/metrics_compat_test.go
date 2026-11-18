package live

import (
	"testing"

	"github.com/colony-2/c2j/pkg/task"
	"github.com/stretchr/testify/require"
)

func TestStoryReadsBothActivityEnvelopeVersions(t *testing.T) {
	for _, raw := range []string{
		`{"v":1,"kind":"activity_output","payload":{"output":{"metrics":"business"}}}`,
		`{"v":2,"kind":"activity_output","payload":{"output":{"metrics":"business"}},"metrics":{"producer":{"status":"absent"},"runtime":{"status":"absent"}}}`,
	} {
		kind, ok := outputKindFromRaw([]byte(raw))
		require.True(t, ok)
		require.Equal(t, task.OutputKindActivityInvocationOutput, kind)
		payload, ok := unwrapTaskEnvelopePayload([]byte(raw))
		require.True(t, ok)
		require.Equal(t, map[string]any{"output": map[string]any{"metrics": "business"}}, payload)
	}
	_, ok := outputKindFromRaw([]byte(`{"v":99,"kind":"activity_output","payload":{}}`))
	require.False(t, ok)
}
