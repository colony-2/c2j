package ops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestValueInvocationTransport(t *testing.T) {
	for _, size := range []int{0, 512 * 1024, 2 * 1024 * 1024} {
		body := strings.Repeat("x", size) + "\n${{ literal }}"
		ref, _, err := artifacts.ParseValue(map[string]any{"kind": "value", "format": "text", "value": body})
		require.NoError(t, err)
		original := ActivityInvocationRequest{Const: true, Input: map[string]any{"text": body}, Artifacts: map[string]artifacts.Ref{"doc.txt": ref}}
		data, err := NewInvocationTaskData(original)
		require.NoError(t, err)
		raw, err := data.GetData()
		require.NoError(t, err)
		require.Less(t, len(raw), 128*1024)
		attached, err := data.GetArtifacts()
		require.NoError(t, err)
		for i := 0; i < 2; i++ {
			var restored ActivityInvocationRequest
			require.NoError(t, json.Unmarshal(raw, &restored))
			if size > 0 {
				require.NotEmpty(t, restored.InputArtifact)
			} else {
				require.Empty(t, restored.InputArtifact)
			}
			// Preserve unrelated input attachments, and remove only the private bundle.
			extra := jobdb.NewArtifactFromBytes("ordinary.txt", []byte("existing input"))
			remaining, err := restored.RestoreInput(context.Background(), append(append([]jobdb.Artifact{}, attached...), extra))
			require.NoError(t, err)
			require.Equal(t, []jobdb.Artifact{extra}, remaining)
			require.Equal(t, original, restored)
		}
	}
	for _, attached := range [][]jobdb.Artifact{nil, {jobdb.NewArtifactFromBytes("bulk.json", []byte("not JSON"))}} {
		req := ActivityInvocationRequest{InputArtifact: "bulk.json"}
		_, err := req.RestoreInput(context.Background(), attached)
		require.Error(t, err)
	}
}
