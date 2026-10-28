package ops

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/git/gitstate"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRepositoryPackageScopePrecedesExistingScopes(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".c2"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".c2/config.yaml"), []byte("packages: [uv:repo==1]"), 0600))
	inv := ActivityInvocationRequest{}
	req := ToolSetupRequest{Scopes: []toolenv.Scope{{ID: "recipe", Packages: []string{"uv:repo==2"}}, {ID: "op", Packages: []string{"uv:repo==3"}}}}
	err := InstallWorkspaceInvocation(context.Background(), &inv, req, root, func(_ context.Context, got ToolSetupRequest) ToolSetupResult {
		require.Equal(t, []toolenv.Scope{{ID: "repository:root", Packages: []string{"uv:repo==1"}}, req.Scopes[0], req.Scopes[1]}, got.Scopes)
		return ToolSetupResult{}
	})
	require.NoError(t, err)
	require.Len(t, req.Scopes, 2)
	require.Equal(t, []string{"uv:repo==1"}, inv.Setup.Diagnostics.Repository.Packages)
}

func TestRepositoryOnlyPreparationFailureAttachesDiagnostics(t *testing.T) {
	for _, config := range []string{"packages: [42]", "packages: [unknown:tool]", "packages: [uv:tool==1]"} {
		t.Run(config, func(t *testing.T) {
			baseRepo, _, cleanup := setupGitRepo(t)
			defer cleanup()
			require.NoError(t, os.Mkdir(filepath.Join(baseRepo, ".c2"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(baseRepo, ".c2/config.yaml"), []byte(config), 0600))
			// setupGitRepo uses real git; record the config in the input snapshot.
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", baseRepo}, args...)...)
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, string(out))
			}
			git("add", ".")
			git("commit", "-m", "packages")
			manager := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(manager, "uv"), []byte("#!/bin/sh\necho simulated-install-failure >&2\nexit 9\n"), 0700))
			t.Setenv("PATH", manager+":"+os.Getenv("PATH"))
			t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
			reg := ActivityRegistration{Step: coreops.TaskStep{Invoke: func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error) {
				t.Fatal("op body ran after setup failure")
				return nil, nil
			}}}
			worker := taskWorker{reg: reg, doer: &opExecutor{reg: reg, deps: coreops.NewServiceDepsBuilder().Build(), controller: gitstate.NewController(nil)}}
			inv := ActivityInvocationRequest{Input: map[string]any{}, GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: baseRepo, BaseRef: "HEAD"}}
			tc := jobworkflow.NewTaskContext(jobdb.JobKey{TenantId: "test", JobId: "repo-failure"}, 1, nil, nil, nil)
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
				b, err := artifact.Bytes(context.Background())
				require.NoError(t, err)
				var out ActivityInvocationOutput
				require.NoError(t, json.Unmarshal(b, &out))
				require.NotEmpty(t, out.Setup.Error)
				require.NotEmpty(t, out.Setup.Repository.Digest)
			}
			require.True(t, found)
		})
	}
}
