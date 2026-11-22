package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/nixtest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/shellcmd"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRealRepositoryNativePackages(t *testing.T) {
	// Run signed-cache failures in independent consumers; the ordinary native
	// container below never tries to launch nested Docker.
	if os.Getenv("C2J_NIX_TEST_CONTAINER") != "1" || os.Getenv("C2J_NIX_SUBSTITUTION_CASE") != "" {
		t.Run("substitution_failure", func(t *testing.T) {
			if !nixtest.InSubstitutionConsumer(t, "../../toolenv/testdata/nix-substitution", []string{"missing", "untrusted"}) {
				return
			}
			testRepositorySubstitutionFailure(t)
		})
		if os.Getenv("C2J_NIX_SUBSTITUTION_CASE") != "" {
			return
		}
	}
	if !nixtest.InContainer(t) {
		return
	}
	for _, compiler := range []string{"implicit", "declared"} {
		t.Run(compiler, func(t *testing.T) { testRealRepositoryNativePackages(t, compiler == "declared") })
	}
}

func testRepositorySubstitutionFailure(t *testing.T) {
	fixture := nixtest.SignedCache(t)
	cache := t.TempDir()
	t.Setenv("C2J_TOOL_CACHE_DIR", cache)
	repo := t.TempDir()
	workspaceRepo(t, repo, "substitution failure")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".c2"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".c2/config.yaml"), []byte(fmt.Sprintf("packages: [%q]\n", "nix:"+fixture.FlakeURL)), 0600))
	require.NoError(t, runGit(repo, "git", "add", "."))
	require.NoError(t, runGit(repo, "git", "commit", "-m", "substitution failure"))
	marker := filepath.Join(t.TempDir(), "command-body")
	rec, err := recipe.LoadRecipeFromString([]byte(fmt.Sprintf(`id: substitution-failure
version: "1"
op: command_execution
inputs:
  shell: sh
  run: |
    echo ran > %s
`, shellcmd.QuotePOSIX(marker))))
	require.NoError(t, err)
	ws := workspaceTestWorker(t, commandop.GetOp())
	eng := newToyEngineWithWorkSet(t, "substitution-failure", ws, nil)
	ctx, cancel := context.WithTimeout(celltest.Context(t.Context()), 2*time.Minute)
	defer cancel()
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "native"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	celltest.Create(t, eng, "substitution-failure", "test", celltest.Repo(repo))
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "substitution-failure", RecipeName: "substitution-failure", JobContext: job, GitRef: "main", Cell: "test", CellRepo: celltest.Repo(repo)}, eng, *rec)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 2*time.Minute, ref.Key(), eng))
	history, err := eng.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: ref.Key(), IncludeOutputs: true, IncludeArtifacts: true})
	require.NoError(t, err)
	completed, err := eng.GetJob(ctx, ref.Key())
	require.NoError(t, err)
	require.Equal(t, "failed_app", completed.CompletionStatus)
	found := false
	for _, task := range executionTaskRuns(history) {
		if task.TaskType != "command_execution:command_execution" {
			continue
		}
		for _, attempt := range task.Attempts {
			if attempt.Output == nil {
				continue
			}
			for _, artifact := range attempt.Output.Artifacts {
				if artifact.Name != "_c2j/preparation.json" {
					continue
				}
				found = true
				require.NotNil(t, artifact.Key)
				raw, err := artifact.Key.ToLazyArtifact(eng, ref.Key().TenantId).Bytes(ctx)
				require.NoError(t, err)
				var diagnostic workerops.ActivityInvocationOutput
				require.NoError(t, json.Unmarshal(raw, &diagnostic))
				require.NotNil(t, diagnostic.Setup)
				require.NotEmpty(t, diagnostic.Setup.Error)
				require.Len(t, diagnostic.Setup.Tools, 1, "the declared fixture must reach tool preparation")
				require.Equal(t, "failed", diagnostic.Setup.Tools[0].Outcome)
				if os.Getenv("C2J_NIX_SUBSTITUTION_CASE") == "untrusted" {
					require.Contains(t, diagnostic.Setup.Error, "not signed by any of the keys in 'trusted-public-keys'")
				} else {
					require.Contains(t, diagnostic.Setup.Error, fixture.Paths[2])
					require.Contains(t, diagnostic.Setup.Error, "no substituter that can build it")
				}
				t.Logf("retained preparation failure: %s", diagnostic.Setup.Error)
			}
		}
	}
	require.True(t, found, "failed task must retain preparation diagnostics")
	_, err = os.Stat(marker)
	require.True(t, os.IsNotExist(err), "command body must not execute")
	manifests, err := filepath.Glob(filepath.Join(cache, "packages", "*", "ready.json"))
	require.NoError(t, err)
	require.Empty(t, manifests)
	nixtest.AssertNoBuilders(t)
}

func testRealRepositoryNativePackages(t *testing.T, declared bool) {
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	repo := t.TempDir()
	workspaceRepo(t, repo, "native")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".c2"), 0700))
	// binutils is GNU but is not a compiler: activation must still provide cc.
	const pin = "nix:github:NixOS/nixpkgs/b6018f87da91d19d0ab4cf979885689b469cdd41#"
	packages := "packages:\n  - " + pin + "openssl\n  - " + pin + "zlib\n  - " + pin + "binutils\n"
	if declared {
		packages += "  - " + pin + "stdenv.cc\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".c2/config.yaml"), []byte(packages), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "probe.c"), []byte(`#include <openssl/crypto.h>
#include <zlib.h>
#include <stdio.h>
int main(void) {
 if (!OpenSSL_version_num() || !zlibVersion()) return 1;
 puts("repository-native-ok");
 return 0;
}
`), 0600))
	require.NoError(t, runGit(repo, "git", "add", "."))
	require.NoError(t, runGit(repo, "git", "commit", "-m", "native dependencies"))
	ws := workspaceTestWorker(t, commandop.GetOp())
	eng := newToyEngineWithWorkSet(t, "repository-native", ws, nil)
	rec, err := recipe.LoadRecipeFromString([]byte(`id: repository-native
version: "1"
op: command_execution
inputs:
  run: |
    set -eu
    cc probe.c -o probe $(pkg-config --cflags --libs openssl zlib)
    ./probe
`))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(celltest.Context(t.Context()), 5*time.Minute)
	defer cancel()
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "native"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	celltest.Create(t, eng, "repository-native", "test", celltest.Repo(job.GitBase.BaseRepo))
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "repository-native", RecipeName: "repository-native", JobContext: job, GitRef: "main", Cell: "test", CellRepo: celltest.Repo(job.GitBase.BaseRepo)}, eng, *rec)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 5*time.Minute, ref.Key(), eng))
	result, err := swfutil.JobResult(ctx, eng, ref.Key())
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	require.Contains(t, string(raw), `"stdout":"repository-native-ok"`)
}
