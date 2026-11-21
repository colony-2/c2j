package ops

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestArtifactFilePreparationCollectsDefaultsAndJSONKeys(t *testing.T) {
	key := jobdb.ArtifactKey{JobId: "producer", TaskOrdinal: 0, Name: "same", SizeBytes: -1}
	other := key
	other.TaskOrdinal = 1
	ref := artifacts.NewStoredRef(key)
	for _, asJSON := range []bool{false, true} {
		var value any = ref
		if asJSON {
			raw, err := json.Marshal(ref)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &value))
		}
		spec := extops.ExtensionOpSpec{InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"instructions_file":  map[string]any{"type": "object", "x-c2j-artifact-file": true, "default": value},
			"prompt_prefix_file": map[string]any{"type": "object", "x-c2j-artifact-file": true},
		}}}
		op, err := extops.RestoreResolvedOp(&extops.ResolvedOp{Spec: spec})
		require.NoError(t, err)
		// A precollected handle with different size metadata is the same storage
		// identity. Keep its key and retain the public reference's own metadata.
		collected := key
		collected.SizeBytes = 123
		inv := ActivityInvocationRequest{Input: map[string]any{"inputs": map[string]any{"prompt_prefix_file": value}}, ArtifactKeys: []jobdb.ArtifactKey{other, collected}}
		req := ToolSetupRequest{Extension: op}
		// ResolveInvocation applies ordinary manifest defaults before installation.
		_, err = op.ApplyInvocationDefaults(inv.Input["inputs"].(map[string]any))
		require.NoError(t, err)
		require.NoError(t, validatePreparedInputs(&inv, req))
		require.NoError(t, validatePreparedInputs(&inv, req))
		require.Equal(t, []jobdb.ArtifactKey{other, collected}, inv.ArtifactKeys)
		actual, err := artifacts.ParseFileRef(inv.Input["inputs"].(map[string]any)["instructions_file"])
		require.NoError(t, err)
		require.Equal(t, key, actual.Stored.Key)
		fresh := ActivityInvocationRequest{Input: map[string]any{"inputs": map[string]any{"prompt_prefix_file": value}}, ArtifactKeys: []jobdb.ArtifactKey{other}}
		_, err = op.ApplyInvocationDefaults(fresh.Input["inputs"].(map[string]any))
		require.NoError(t, err)
		require.NoError(t, validatePreparedInputs(&fresh, req))
		require.Equal(t, []jobdb.ArtifactKey{other, key}, fresh.ArtifactKeys)
		raw, err := json.Marshal(inv)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "path")
		require.Contains(t, string(raw), "producer")
	}
}

func TestArtifactFileRejectedBeforeToolInstallation(t *testing.T) {
	op, err := extops.RestoreResolvedOp(&extops.ResolvedOp{Spec: extops.ExtensionOpSpec{InputSchema: map[string]any{"type": "object", "properties": map[string]any{"instructions_file": map[string]any{"type": "object", "x-c2j-artifact-file": true}}}}})
	require.NoError(t, err)
	inv := ActivityInvocationRequest{Input: map[string]any{"inputs": map[string]any{"instructions_file": "/tmp/raw-path"}}}
	called := false
	err = InstallInvocation(context.Background(), &inv, ToolSetupRequest{Extension: op}, func(context.Context, ToolSetupRequest) ToolSetupResult { called = true; return ToolSetupResult{} })
	require.ErrorContains(t, err, "inputs.instructions_file")
	require.False(t, called)
}
