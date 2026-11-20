package ops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/pnpmtest"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestPnpmWorkerWorkspacePreparation(t *testing.T) {
	if os.Getenv("C2J_TEST_REAL_PNPM") != "1" {
		t.Skip("set C2J_TEST_REAL_PNPM=1 to run actual workspace preparation")
	}
	archive := pnpmtest.Archive(t, t.TempDir(), "c2j-worker-probe", "1.0.0", pnpmtest.BuildScript, nil, nil)
	archive = pnpmtest.Serve(t, archive)
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".c2"), 0700))
	config, err := json.Marshal(map[string]any{"packages": []string{"pnpm:" + archive}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".c2/config.yaml"), config, 0600))
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	ctx := celltest.Context(context.Background())
	var inv ActivityInvocationRequest
	require.NoError(t, InstallWorkspaceInvocation(ctx, &inv, ToolSetupRequest{}, root, PrepareTools))
	require.True(t, inv.Setup.Environment.Ready())
	require.Equal(t, "prepared", inv.Setup.Diagnostics.Tools[0].Outcome)
	require.NotEmpty(t, inv.Setup.Diagnostics.Tools[0].Pnpm.Version)
	out, stderr, err := process.ExecuteProcess(inv.Setup.Environment.Bind(ctx), process.RunRequest{WorkingDir: t.TempDir(), Command: []string{"c2j-worker-probe"}})
	require.NoError(t, err, string(stderr))
	require.Equal(t, "c2j-worker-probe@1.0.0\n", string(out))
	target := filepath.Join(inv.Setup.Environment.Tools[0].Bin, "c2j-worker-probe")
	require.NoError(t, os.Remove(target))
	require.False(t, inv.Setup.Environment.Ready())
	require.NoError(t, InstallWorkspaceInvocation(ctx, &inv, ToolSetupRequest{}, root, PrepareTools))
	require.True(t, inv.Setup.Environment.Ready())
	require.Equal(t, "prepared", inv.Setup.Diagnostics.Tools[0].Outcome)
}

func TestPnpmWorkerFailureBeforeBody(t *testing.T) {
	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()
	bin := t.TempDir()
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Model an explicit effective denial; actual PrepareTools/RunTask must retain
	// the pnpm-specific failure before the consuming operation can start.
	require.NoError(t, os.WriteFile(filepath.Join(bin, "pnpm"), []byte(`#!/bin/sh
case "$1" in
 --version) echo 12.21.0;;
 config) echo '{"permissions":{"denied":{"build":false}}}';;
 *) exit 99;;
esac
`), 0700))
	inv := ActivityInvocationRequest{Input: map[string]any{}, Preparation: PreparationRequest{Scopes: []toolenv.Scope{{ID: "op", Packages: []string{"pnpm:denied@1"}}}}}
	inv.GitTaskContext = gitstate.GlobalGitTaskContext{BaseRepo: baseRepo, BaseRef: baseHash}
	worker := taskWorker{doer: &opExecutor{deps: coreops.NewServiceDepsBuilder().Build(), controller: gitstate.NewController(nil)}}
	tc := jobworkflow.NewTaskContext(jobdb.JobKey{TenantId: "test", JobId: "pnpm-denial"}, 7, nil, nil, nil)
	data, err := worker.RunTask(deniedTaskContext(t, tc), tc, jobdb.NewTaskDataOrPanic(inv))
	require.ErrorContains(t, err, "conflicting pnpm setting permissions")
	require.NotNil(t, data)
	artifacts, err := data.GetArtifacts()
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	raw, err := artifacts[0].Bytes(celltest.Context(context.Background()))
	require.NoError(t, err)
	var result ActivityInvocationOutput
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Equal(t, "failed", result.Setup.Tools[0].Outcome)
	require.Equal(t, "12.21.0", result.Setup.Tools[0].Pnpm.Version)
	require.Equal(t, "failed", result.Setup.Tools[0].Pnpm.Validation)
}
