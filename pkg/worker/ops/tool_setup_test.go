package ops

import (
	"context"
	"encoding/json"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestToolSetupFailureDiagnostics(t *testing.T) {
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	result := PrepareTools(context.Background(), ToolSetupRequest{Scopes: []toolenv.Scope{{ID: "recipe", Packages: []string{"unsupported:tool"}}}})
	require.Contains(t, result.Diagnostics.Error, "unsupported package prefix")
	require.Len(t, result.Diagnostics.Tools, 1)
	require.Equal(t, "failed", result.Diagnostics.Tools[0].Outcome)
}
func TestExtensionResolutionFailure(t *testing.T) {
	inv := ActivityInvocationRequest{Preparation: PreparationRequest{Extension: &ExtensionResolutionRequest{Selector: "./missing", Options: extops.ResolveOptions{BaseDir: t.TempDir()}}}}
	_, err := ResolveInvocation(context.Background(), &inv)
	require.ErrorContains(t, err, "extension setup failed")
}

func TestPreparationFailureAttachesOutcomeDiagnostics(t *testing.T) {
	inv := ActivityInvocationRequest{Input: map[string]any{}, Preparation: PreparationRequest{Scopes: []toolenv.Scope{{ID: "test", Packages: []string{"unsupported:tool"}}}}}
	worker := taskWorker{sessions: secrets.NewSessions()}
	data, err := worker.Run(jobworkflow.TaskContext{Step: 7}, jobdb.NewTaskDataOrPanic(inv))
	require.ErrorContains(t, err, "unsupported package prefix")
	require.NotNil(t, data)
	artifacts, err := data.GetArtifacts()
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	raw, err := artifacts[0].Bytes(context.Background())
	require.NoError(t, err)
	var diagnostic ActivityInvocationOutput
	require.NoError(t, json.Unmarshal(raw, &diagnostic))
	require.NotNil(t, diagnostic.Setup)
	require.EqualValues(t, 7, *diagnostic.Setup.TaskOrdinal)
	require.Contains(t, diagnostic.Setup.Error, "unsupported package prefix")
}
