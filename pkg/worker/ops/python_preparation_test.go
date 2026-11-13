package ops

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestPythonPreparationFailureStopsBodyAndRetainsUVError(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv required")
	}
	repo, hash, cleanup := setupGitRepo(t)
	defer cleanup()
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	t.Setenv("UV_CACHE_DIR", t.TempDir())
	t.Setenv("UV_PYTHON_DOWNLOADS", "never")
	reg := ActivityRegistration{Step: coreops.TaskStep{Invoke: func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error) {
		t.Fatal("body ran after Python preparation failure")
		return nil, nil
	}}}
	worker := taskWorker{reg: reg, doer: &opExecutor{reg: reg, deps: coreops.NewServiceDepsBuilder().Build(), controller: gitstate.NewController(nil)}}
	inv := ActivityInvocationRequest{Input: map[string]any{}, GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: repo, BaseRef: hash}, Preparation: PreparationRequest{Scopes: []toolenv.Scope{{ID: "python", Python: &execution.Python{RequiresPython: ">=99", Dependencies: []string{}}}}}}
	tc := jobworkflow.NewTaskContext(jobdb.JobKey{TenantId: "test", JobId: "python-failure"}, 1, nil, nil, nil)
	data, err := worker.RunTask(deniedTaskContext(t, tc), tc, jobdb.NewTaskDataOrPanic(inv))
	require.Error(t, err)
	require.NotNil(t, data)
	artifacts, err := data.GetArtifacts()
	require.NoError(t, err)
	found := false
	for _, artifact := range artifacts {
		if artifact.Name() != "_c2j/preparation.json" {
			continue
		}
		found = true
		raw, err := artifact.Bytes(celltest.Context(context.Background()))
		require.NoError(t, err)
		var out ActivityInvocationOutput
		require.NoError(t, json.Unmarshal(raw, &out))
		require.Equal(t, "failed", out.Setup.Python.Outcome)
		require.NotEmpty(t, out.Setup.Python.CommandLogs)
		require.Contains(t, out.Setup.Error, "uv")
	}
	require.True(t, found)
	entries, err := filepath.Glob(filepath.Join(os.Getenv("C2J_TOOL_CACHE_DIR"), "python", "*", "ready.json"))
	require.NoError(t, err)
	require.Empty(t, entries)
}
