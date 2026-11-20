package recipetest

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/colony-2/c2j/internal/pnpmtest"
	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestPnpmNativeRecipePassthrough(t *testing.T) {
	if os.Getenv("C2J_TEST_REAL_PNPM") != "1" {
		t.Skip("set C2J_TEST_REAL_PNPM=1 to exercise actual pnpm preparation")
	}
	withRegisteredCoreOps(t, commandop.GetOp())
	deps := coreops.NewServiceDepsBuilder().Build()
	c := Case{ID: "pnpm", Type: "recipe_case", Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: "passthrough"}}}}}
	j := newTestJobContext("test", c, TestPolicy{}, deps)
	j.operationPaths = recipeTestOperationPaths(t.TempDir())
	require.NoError(t, ensureRecipeTestOperationDirs(j.operationPaths))
	archive := pnpmtest.Archive(t, t.TempDir(), "c2j-passthrough", "1.0.0", pnpmtest.BuildScript, nil, nil)
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	encoded, err := json.Marshal(map[string]any{"id": "pnpm-passthrough", "op": "command_execution", "execution": map[string]any{"packages": []string{"pnpm:" + archive}}, "inputs": map[string]any{"run": "c2j-passthrough"}})
	require.NoError(t, err)
	rec := mustLoadRecipe(t, string(encoded))
	out, _, err := compiler.ExecuteRecipe(workflow.Context{JobContext: j, ServiceDependencies2: deps}, *rec, nil, contextual.JobContext{Environment: recipeTestEnvironment(j.operationPaths)}, contextual.GitCommitContext{})
	require.NoError(t, err)
	require.Equal(t, "c2j-passthrough@1.0.0", out["stdout"])
	// A fresh native consuming task with script suppression must fail preparation.
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	t.Setenv("PNPM_CONFIG_IGNORE_SCRIPTS", "true")
	j = newTestJobContext("test", c, TestPolicy{}, deps)
	j.operationPaths = recipeTestOperationPaths(t.TempDir())
	require.NoError(t, ensureRecipeTestOperationDirs(j.operationPaths))
	_, _, err = compiler.ExecuteRecipe(workflow.Context{JobContext: j, ServiceDependencies2: deps}, *rec, nil, contextual.JobContext{Environment: recipeTestEnvironment(j.operationPaths)}, contextual.GitCommitContext{})
	require.ErrorContains(t, err, "conflicting pnpm setting ignorescripts")
}
