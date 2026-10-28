package compiler

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/nixtest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/c2j/pkg/workflowctl"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRealRepositoryNativePackages(t *testing.T) {
	if !nixtest.InContainer(t) {
		return
	}
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	repo := t.TempDir()
	workspaceRepo(t, repo, "native")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".c2"), 0700))
	// binutils is GNU but is not a compiler: activation must still provide cc.
	const pin = "nix:github:NixOS/nixpkgs/b6018f87da91d19d0ab4cf979885689b469cdd41#"
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".c2/config.yaml"), []byte("packages:\n  - "+pin+"openssl\n  - "+pin+"zlib\n  - "+pin+"binutils\n"), 0600))
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
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "native"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "repository-native", RecipeName: "repository-native", JobContext: job, GitRef: "main"}, eng, *rec)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 5*time.Minute, ref.Key(), eng))
	result, err := swfutil.JobResult(ctx, eng, ref.Key())
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	require.Contains(t, string(raw), `"stdout":"repository-native-ok"`)
}
