# Recipe-child Test Plan

## Scope
Validate the child-recipe ops in `/src/pkg/child` with a mix of unit tests and recipe fixture integration tests.

Primary ops under test:
- `recipe.wait_and_get_result`
- `recipe.get_result`
- `recipe.run_and_wait.start`
- `recipes.start`

Secondary coverage: `recipe.run_and_wait.finish` (same path as `recipe.wait_and_get_result`).

## Unit Tests (Go)
Location suggestion: `pkg/recipe/`

### launcher.go
- `startJobs`
  - Empty recipes returns `error` "no jobs to start".
  - Single recipe calls `ctl.StartJob` once and returns single `JobKey`.
  - Multiple recipes runs in transaction and starts each job with tx context.
  - Error on second job aborts transaction, returns error, no partial results.
  - Guard against panic on slice indexing (current code uses `make([]T, 0, n)` then assigns by index).

### op.go
- `getRecipeOutput`
  - Propagates `WorkflowControl.JobResult` error.
  - Propagates `JobResult.GetData` error.
  - Propagates JSON unmarshal error.
  - Propagates `JobResult.GetArtifacts` error.
  - Calls `deps.AddOutputArtifact` for each artifact in job result.
  - Returns `SingleRecipeOutput.Outputs` from `ActivityInvocationOutput.OpOutput`.
- `waitAndGetRecipeOutput`
  - Calls `JobTool.AwaitJobs` before `JobResult`.
  - If `AwaitJobs` fails, returns error and does not call `JobResult`.
  - If `AwaitJobs` succeeds, behavior matches `getRecipeOutput`.

## Fixture Integration Tests (recipe-worker harness)
Location suggestion:
- Recipes: `/src/pkg/child/test-fixtures/recipes/`
- Test runner: `/src/pkg/ops/recipe_fixtures_test.go`

### Fixture recipes
Primary recipes under test (parent):
- `child-recipe-parent.yaml`
  - Calls `recipe.run_and_wait.start` + `recipe.wait_and_get_result` with inputs and git ref.
- `child-recipes-parent.yaml`
  - Calls `recipes.start` with multiple recipes.

Secondary recipes (children):
- `child-simple.yaml` returns a simple output value.
- `child-artifact.yaml` emits an artifact (to validate artifact forwarding).

### Fixture test cases
- `recipe.run_and_wait.start` + `recipe.wait_and_get_result`:
  - Inputs forwarded to child.
  - Outputs from child appear in parent outputs.
  - Artifact from child is surfaced via `wantArtifacts`.
- `recipe.get_result`:
  - Start child recipe, then call `recipe.get_result` and assert output.
  - Invalid job id returns error and matches `wantErrContains`.
- `recipes.start`:
  - Start multiple child recipes, then wait for results for each.
  - Assert outputs in matching order.
- `git_ref` propagation:
  - Parent provides `git_ref`, child echoes value via output to assert pass-through.

### Test runner
- `recipe_fixtures_test.go` should call:
  - `testfixtures.RunTestOnAllRecipes("test-fixtures/recipes/*.test.yaml", t)`

## Test Data and Context
- Use harness defaults for repo/cell context.
- Keep inputs minimal and deterministic.

## Execution
From `/src/pkg/child`:
- `go test ./...`
