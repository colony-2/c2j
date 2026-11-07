package recipetest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRepositoryPackagesPassthroughAndMockBoundary(t *testing.T) {
	for _, mode := range []string{"passthrough", "return"} {
		t.Run(mode, func(t *testing.T) {
			withRegisteredCoreOps(t, commandop.GetOp())
			deps := coreops.NewServiceDepsBuilder().Build()
			c := Case{ID: "repository", Type: "recipe_case", Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: mode, Outputs: map[string]any{"stdout": "mock"}}}}}}
			j := newTestJobContext("test", c, TestPolicy{}, deps)
			j.operationPaths = recipeTestOperationPaths(t.TempDir())
			require.NoError(t, ensureRecipeTestOperationDirs(j.operationPaths))
			require.NoError(t, os.Mkdir(filepath.Join(j.operationPaths.WorktreePath, ".c2"), 0700))
			content := "packages: [uv:repository-test==1]"
			if mode == "return" {
				content = "packages: [invalid:must-not-be-read]"
			}
			require.NoError(t, os.WriteFile(filepath.Join(j.operationPaths.WorktreePath, ".c2/config.yaml"), []byte(content), 0600))
			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "repository-test"), []byte("#!/bin/sh\nprintf 'repository-tool'"), 0700))
			setups := 0
			j.prepareTools = func(_ context.Context, req workerops.ToolSetupRequest) workerops.ToolSetupResult {
				setups++
				require.Equal(t, []toolenv.Scope{{ID: "repository:root", Packages: []string{"uv:repository-test==1"}}}, req.Scopes)
				return workerops.ToolSetupResult{Environment: &toolenv.Environment{Path: bin}}
			}
			rec := mustLoadRecipe(t, "id: repository\nop: command_execution\ninputs: {run: repository-test}\n")
			out, _, err := compiler.ExecuteRecipe(workflow.Context{JobContext: j, ServiceDependencies2: deps}, *rec, nil, contextual.JobContext{Environment: recipeTestEnvironment(j.operationPaths)}, contextual.GitCommitContext{})
			require.NoError(t, err)
			if mode == "return" {
				require.Zero(t, setups)
				require.Equal(t, "mock", out["stdout"])
			} else {
				require.Equal(t, 1, setups)
				require.Equal(t, "repository-tool", out["stdout"])
			}
		})
	}
}

func TestDeclaredRunnerPassthroughAndMockBoundary(t *testing.T) {
	for _, mode := range []string{"passthrough", "return"} {
		t.Run(mode, func(t *testing.T) {
			withRegisteredCoreOps(t, commandop.GetOp())
			deps := coreops.NewServiceDepsBuilder().Build()
			c := Case{ID: "runner", Type: "recipe_case", Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: mode, Outputs: map[string]any{"stdout": "mock"}}}}}}
			j := newTestJobContext("test", c, TestPolicy{}, deps)
			j.operationPaths = recipeTestOperationPaths(t.TempDir())
			require.NoError(t, ensureRecipeTestOperationDirs(j.operationPaths))
			require.NoError(t, os.Mkdir(filepath.Join(j.operationPaths.WorktreePath, ".c2"), 0700))
			config := "packages: [uv:runner==1]\n"
			if mode == "return" {
				config = "packages: [invalid:must-not-be-read]\n"
			}
			require.NoError(t, os.WriteFile(filepath.Join(j.operationPaths.WorktreePath, ".c2/config.yaml"), []byte(config), 0600))
			base := t.TempDir()
			t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
			require.NoError(t, os.WriteFile(filepath.Join(base, "uv"), []byte(`#!/bin/sh
set -eu
mkdir -p "$UV_TOOL_BIN_DIR"
printf '#!/bin/sh\necho declared-pnpm\n' > "$UV_TOOL_BIN_DIR/pnpm"
chmod +x "$UV_TOOL_BIN_DIR/pnpm"
`), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(base, "pnpm"), []byte("#!/bin/sh\necho ambient-pnpm\n"), 0700))
			t.Setenv("PATH", base+string(os.PathListSeparator)+os.Getenv("PATH"))
			setups := 0
			j.prepareTools = func(ctx context.Context, req workerops.ToolSetupRequest) workerops.ToolSetupResult {
				setups++
				return workerops.PrepareTools(ctx, req)
			}
			rec := mustLoadRecipe(t, "id: runner\nop: command_execution\ninputs: {run: 'pnpm --version'}\n")
			out, _, err := compiler.ExecuteRecipe(workflow.Context{JobContext: j, ServiceDependencies2: deps}, *rec, nil, contextual.JobContext{Environment: recipeTestEnvironment(j.operationPaths)}, contextual.GitCommitContext{})
			require.NoError(t, err)
			if mode == "return" {
				require.Zero(t, setups)
				require.Equal(t, "mock", out["stdout"])
			} else {
				require.Equal(t, 1, setups)
				require.Equal(t, "declared-pnpm", out["stdout"])
			}
		})
	}
}
