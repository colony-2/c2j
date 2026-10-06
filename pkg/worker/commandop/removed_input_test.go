package commandop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestCommandUnknownSandboxInputNeverExecutes(t *testing.T) {
	for _, value := range []any{map[string]any{"type": "shai"}, map[string]any{"type": "none"}, map[string]any{}, nil, false, 123, "${{ inputs.mode }}", []any{"shai"}} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			root := t.TempDir()
			raw := map[string]any{"run": "echo executed > marker", "working_directory": root, "continue_on_error": true, "sandbox": value}
			// The same decoder also receives persisted invocation maps.
			data, err := json.Marshal(raw)
			require.NoError(t, err)
			var restored map[string]any
			require.NoError(t, json.Unmarshal(data, &restored))
			output, err := GetOp().TaskChain()[0].Invoke(ops.NewOpDependenciesBuilder().Build(), context.Background(), restored)
			require.ErrorContains(t, err, "invalid keys: sandbox")
			require.Nil(t, output)
			_, err = os.Stat(filepath.Join(root, "marker"))
			require.True(t, os.IsNotExist(err))
		})
	}
}
