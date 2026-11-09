package recipetest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRepositoryExecutionPassthroughAdmissionAndMocks(t *testing.T) {
	for _, mode := range []string{"passthrough", "return"} {
		t.Run(mode, func(t *testing.T) {
			withRegisteredCoreOps(t, commandop.GetOp())
			deps := coreops.NewServiceDepsBuilder().Build()
			c := Case{ID: "repository", Type: "recipe_case", Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: mode, Outputs: map[string]any{"stdout": "mock"}}}}}}
			j := newTestJobContext("test", c, TestPolicy{}, deps)
			j.operationPaths = recipeTestOperationPaths(t.TempDir())
			require.NoError(t, ensureRecipeTestOperationDirs(j.operationPaths))
			require.NoError(t, os.Mkdir(filepath.Join(j.operationPaths.WorktreePath, ".c2"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(j.operationPaths.WorktreePath, ".c2/config.yaml"), []byte("execution: {resources: {memory: 8Gi}}\npackages: [uv:tool==1]"), 0600))
			j.prepareTools = func(context.Context, workerops.ToolSetupRequest) workerops.ToolSetupResult {
				t.Fatal("tools ran without admission")
				return workerops.ToolSetupResult{}
			}
			rec := mustLoadRecipe(t, "id: repository\nop: command_execution\ninputs: {run: 'touch should-not-run'}\n")
			out, _, err := compiler.ExecuteRecipe(workflow.Context{JobContext: j, ServiceDependencies2: deps}, *rec, nil, contextual.JobContext{Environment: recipeTestEnvironment(j.operationPaths)}, contextual.GitCommitContext{})
			if mode == "return" {
				require.NoError(t, err)
				require.Equal(t, "mock", out["stdout"])
			} else {
				require.ErrorContains(t, err, "execution-aware runtime")
			}
			_, err = os.Stat(filepath.Join(j.operationPaths.WorktreePath, "should-not-run"))
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestRepositoryExecutionPassthroughPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, declarations string
		allowed            bool
	}{
		{"absent", "", false},
		{"recipe clears fallback", "execution_needs: {base: '*', capabilities: []}\n", true},
		{"node clears fallback", "NODE", true},
		{"outer capability remains", "execution_needs: {base: '*', capabilities: [outer]}\n", false},
		{"lower scalar still constrained", "execution_needs: {base: '*', capabilities: [], resources: {memory: 1Gi}}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withRegisteredCoreOps(t, commandop.GetOp())
			deps := coreops.NewServiceDepsBuilder().Build()
			c := Case{ID: "precedence", Type: "recipe_case", Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: "passthrough"}}}}}
			j := newTestJobContext("test", c, TestPolicy{}, deps)
			j.operationPaths = recipeTestOperationPaths(t.TempDir())
			require.NoError(t, ensureRecipeTestOperationDirs(j.operationPaths))
			require.NoError(t, os.Mkdir(filepath.Join(j.operationPaths.WorktreePath, ".c2"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(j.operationPaths.WorktreePath, ".c2/config.yaml"), []byte("execution: {base: linux*, capabilities: [docker]}\npackages: [uv:tool==1]"), 0600))
			installs := 0
			j.prepareTools = func(context.Context, workerops.ToolSetupRequest) workerops.ToolSetupResult {
				installs++
				return workerops.ToolSetupResult{}
			}
			source := "id: precedence\n" + tc.declarations + "sequence:\n - op: command_execution\n   execution_needs: {capabilities: []}\n   inputs: {run: 'touch did-run'}\n"
			if tc.declarations == "NODE" {
				source = "id: precedence\nsequence:\n - op: command_execution\n   execution_needs: {base: '*', capabilities: []}\n   inputs: {run: 'touch did-run'}\n"
			}
			rec := mustLoadRecipe(t, source)
			_, _, err := compiler.ExecuteRecipe(workflow.Context{JobContext: j, ServiceDependencies2: deps}, *rec, nil, contextual.JobContext{Environment: recipeTestEnvironment(j.operationPaths)}, contextual.GitCommitContext{})
			_, effectErr := os.Stat(filepath.Join(j.operationPaths.WorktreePath, "did-run"))
			if tc.allowed {
				require.NoError(t, err)
				require.Equal(t, 1, installs)
				require.NoError(t, effectErr)
			} else {
				require.ErrorContains(t, err, "execution-aware runtime")
				require.Zero(t, installs)
				require.True(t, os.IsNotExist(effectErr))
			}
		})
	}
}
