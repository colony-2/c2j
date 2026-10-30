package ops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/stretchr/testify/require"
)

func TestValueArtifactMaterialization(t *testing.T) {
	for _, size := range []int{453842, 512 * 1024, 2 * 1024 * 1024} {
		content := strings.Repeat("x", size-25) + "\n${{ untouched }}\n$(false)"
		ref, _, err := artifacts.ParseValue(map[string]any{"kind": "value", "format": "text", "value": content})
		require.NoError(t, err)
		// The same serialized descriptor can materialize in a replacement worker's
		// fresh inbox without relying on a previous local path or artifact store.
		for i := 0; i < 2; i++ {
			inbox := t.TempDir()
			require.NoError(t, materializeArtifactBindings(context.Background(), inbox, map[string]artifacts.Ref{"docs/context.txt": ref}, nil))
			data, err := os.ReadFile(filepath.Join(inbox, "docs/context.txt"))
			require.NoError(t, err)
			require.Equal(t, content, string(data))
			require.Error(t, materializeArtifactBindings(context.Background(), inbox, map[string]artifacts.Ref{"docs/context.txt": ref}, nil), "must not overwrite")
		}
		require.Error(t, materializeArtifactBindings(context.Background(), t.TempDir(), map[string]artifacts.Ref{"../escape": ref}, nil))
	}
}
