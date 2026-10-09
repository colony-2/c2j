package ops

import (
	"encoding/json"
	"testing"

	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestToolSetupFailureHasDurableDiagnostics(t *testing.T) {
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	data, err := NewToolSetupWorker().Run(jobworkflow.TaskContext{Step: 42}, jobdb.NewTaskDataOrPanic(ToolSetupRequest{Scopes: []toolenv.Scope{{ID: "recipe", Packages: []string{"unsupported:tool"}}}}))
	require.NoError(t, err)
	raw, err := data.GetData()
	require.NoError(t, err)
	var result ToolSetupResult
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Contains(t, result.Diagnostics.Error, "unsupported package prefix")
	require.EqualValues(t, 42, *result.Diagnostics.TaskOrdinal)
	require.Len(t, result.Diagnostics.Tools, 1)
	require.Equal(t, "failed", result.Diagnostics.Tools[0].Outcome)
	require.False(t, result.Ready())
}

func TestExtensionResolutionFailureRecordsTiming(t *testing.T) {
	data, err := NewExtensionResolutionWorker().Run(jobworkflow.TaskContext{}, jobdb.NewTaskDataOrPanic(ExtensionResolutionRequest{Selector: "./missing", Options: extops.ResolveOptions{BaseDir: t.TempDir()}}))
	require.NoError(t, err)
	raw, err := data.GetData()
	require.NoError(t, err)
	var result ExtensionResolutionResult
	require.NoError(t, json.Unmarshal(raw, &result))
	require.NotEmpty(t, result.Error)
	require.Nil(t, result.Op)
	require.GreaterOrEqual(t, result.WallMS, int64(0))
}
