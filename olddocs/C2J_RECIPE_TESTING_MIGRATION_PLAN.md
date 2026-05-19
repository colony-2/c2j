# Plan: Port Recipe Test Running to `c2j`

## Goal

Move the old recipe testing flow from:

- CLI orchestration in `/colony2/cli/internal/cmd/recipe_testing.go`
- HTTP handlers in `/colony2/server/api/internal/handlers/recipe_testing.go`
- execution implementation in `/colony2/server/api/internal/recipetesting/service.go`

into the local `c2j` command tree in `/src`, with no network hop between the new `c2j` test command and the recipe-test execution implementation.

The new flow should run entirely inside the `c2j` process:

```text
c2j test run -> local suite compiler -> local case worker pool -> local recipetest harness
```

No `POST /recipe-tests/...` HTTP call should exist in the new command path.

## Migration Principle

This is a copy-and-adapt migration, not a rewrite.

Default to copying the existing core implementation from the old CLI and server API, preserving behavior, tests, data shapes, and edge-case handling wherever possible. Reimplement only the seams that cannot carry over:

- old CLI app/config/printer plumbing
- HTTP client code and handler dispatch
- Colony2 server recipe lookup
- project/server-specific types
- runtime root selection for local disposable embedded tests

Everything else should start as a direct port and then receive the smallest changes needed to compile and fit the `c2j` package layout.

Do not preserve the old API shape as an internal abstraction. The old per-case API request/response envelopes should collapse into local inputs and results, and there should be no exported `Service` type. The command is ephemeral, so the durable state is the local result directory, not an in-process service object.

## Recommended Command Shape

Add a top-level `c2j test` command to match the current flat `c2j` command style:

```bash
c2j test compile
c2j test validate
c2j test run
c2j test case validate
c2j test case run
```

Keep the old behavior and flags where they still make sense:

- `--recipe <name-or-selector>`
- `--recipe-file <path>`
- `--file <suite-path>`
- `--stdin`
- `--format <canonical_yaml|canonical_json|compact_yaml|scenario_md>`
- `--case <case-id>` repeatable
- `--strict`
- `--parallelism <n>`
- `--fail-fast`
- `--stop-on-failure`
- `--case-timeout <duration>`
- `--artifact-mode none|inline`
- `--artifact-max-bytes <n>`
- `--out-dir <dir>`
- `--jsonl-events <path>`
- `--evaluation-mode enforce|report_only`
- `--json`

Use c2j-local defaults instead of the old server defaults:

- default output directory: `.c2j/test-results/<timestamp>/`
- default recipe: `default`, using the same current-cell resolution as `c2j submit`
- named recipes resolve from the current cell as `.c2j/recipes/<name>.yaml`
- explicit git selectors continue to use the existing compiler selector syntax
- `--recipe-file` remains the simplest local authoring path
- test commands automatically use embedded SWF mode for any runtime-backed behavior; users should not need to pass `--embed`
- embedded test runtime state should be per-run and disposable by default, not the persistent `~/.c2j/embed/default` state used by normal `submit`/`exec`

## Code Reuse Map

### Old CLI Code To Reuse

Source: `/colony2/cli/internal/cmd/recipe_testing.go`

Keep or port nearly as-is:

- command tree shape for `compile`, `validate`, `run`, and `case`
- shared flag model
- suite loading from file/stdin
- format inference
- fenced-block extraction for `scenario_md`
- case filtering by `--case`
- worker-pool orchestration
- result sorting
- artifact and summary writing
- JSONL event writing
- case ID sanitization

Replace:

- `fetchApp`, `requireProject`, `App`, and old printer usage
- `callRecipeTestEndpoint`
- old HTTP status/network exit behavior
- old `server_ref` wording in user-facing help

### Old Server Code To Reuse

Source: `/colony2/server/api/internal/recipetesting/service.go`

Move the core implementation into `c2j`, preferably as:

```text
/src/pkg/recipetest
```

Reuse most of the core implementation:

- case, target, execution, result, issue, assertion, evaluation, mock, and artifact data shapes
- semantic validation
- case hash calculation
- inline recipe parsing
- dynamic unknown-op stubbing for test-only recipe loading
- isolated mock job context
- mock matching and duplicate mock consumption behavior
- passthrough, record passthrough, and replay behavior
- assertion execution
- text-pattern evaluation
- placeholder LLM judge behavior
- artifact collection and inline artifact truncation
- diagnostics for mock hits and misses

Adapt:

- replace `project.ID` with plain `string`
- replace the old `recipesvc.Service` lookup dependency with a c2j-local recipe resolver function/interface
- remove HTTP body decoding and API envelope boundaries
- inject work directories instead of hardcoding `/tmp/recipe-tests/...`
- expose typed options so the CLI can call package-level validate/run functions directly

Do not move the HTTP handler layer into `c2j`; it is not needed for local execution.

## New Package Layout

### `pkg/recipetest`

Own the local recipe-test harness and public contract. This is not a second c2j job runner, and it should not introduce a long-lived object model; it is a set of ephemeral command helpers that apply mocks, assertions, evaluations, and artifact capture around direct recipe execution.

- `TargetRecipe`
- `Case`
- `ExecutionOptions`
- `ValidationResult`
- `CaseRunResult`
- `Issue`
- `HarnessOptions`
- `RecipeResolver` or `TargetResolver`

Suggested package-level API:

```go
type HarnessOptions struct {
    Resolver    TargetResolver
    Deps        ops.ServiceDependencies2
    CELOptions  template.CELOptionsProvider
    WorkRoot    string
}

func ValidateCase(ctx context.Context, opts HarnessOptions, tenantID string, target TargetRecipe, c Case) ValidationResult
func RunCase(ctx context.Context, opts HarnessOptions, tenantID string, target TargetRecipe, c Case, exec ExecutionOptions) CaseRunResult
```

The implementation can preserve the old prepare/execute split internally if useful, but it should be an unexported helper, not a public boundary.

### `cmd/c2j/internal/testjob`

Own CLI-specific behavior:

- `Options`
- `CompileOptions`
- `RunOptions`
- suite loading
- canonical IR materialization
- parallel scheduling
- live status printing
- local output artifacts
- JSON/JSONL output
- exit-code mapping

This package can import `pkg/recipetest` and `cmd/c2j/internal/c2jops`.

### `cmd/c2j/internal/cmd/test.go`

Wire Cobra commands and flags only. Keep command logic in `internal/testjob`, matching existing `submitjob`, `runjob`, and `listjobs` patterns.

### Shared Target Resolution

The new test command needs the same current-cell behavior as `submit`.

Extract the reusable pieces from `cmd/c2j/internal/submitjob/service.go` into a small internal package, for example:

```text
cmd/c2j/internal/celltarget
```

Move or share:

- current-cell resolution from `.c2j/config.yaml`
- explicit `--cell` resolution
- repository normalization
- default ref selection
- cell name derivation

Then use it from both `submitjob` and `testjob` so recipe names resolve consistently.

## Target Recipe Resolution

Support these target forms:

### `--recipe-file`

Read the local file and create an inline target:

```json
{
  "mode": "inline_recipe",
  "format": "yaml",
  "content": "..."
}
```

This is the closest match to old CLI behavior and should be phase-one complete.

### `--recipe <git-selector>`

Use existing `compiler.NewRecipeSourceResolver` to resolve and load the selector locally.

Examples:

```text
git+https://github.com/acme/demo.git//.c2j/recipes/deploy.yaml@main
git+file:///repo//.c2j/recipes/default.yaml@HEAD
```

### `--recipe <name>`

Resolve through the current cell:

1. resolve the target cell with the same logic as `submit`
2. turn the recipe name into `compiler.BuildCellRecipeSelector(repo, name, ref)`
3. load it through the existing compiler recipe source resolver

This replaces old server recipe lookup. There should be no dependency on Colony2 recipe storage.

## Execution Model

`c2j` already has a job runner in `cmd/c2j/internal/runjob`, and this plan should not duplicate it.

The recipe-test path needs a different execution wrapper because test cases require per-case mocks, isolated policy, op-case scoping, assertion/evaluation handling, and inline artifact capture. The old server implementation handled that by calling direct recipe execution under a custom test `JobContext`.

The new `pkg/recipetest` harness should keep that model and call:

```go
compiler.ExecuteRecipe(...)
```

Do not submit a job to SWF for each test case. The recipe-test harness already provides an isolated `JobContext` that intercepts op execution for mocks, assertions, artifact collection, and policy enforcement.

Reuse existing c2j runner infrastructure only where it is actually shared infrastructure:

- `c2jops.Register()` for the standard op set
- current-cell and recipe selector resolution
- dependency construction patterns from `runjob.buildDeps` for explicit passthrough cases
- embedded SWF/workflow-control wiring, selected automatically for tests

## Embedded Runtime Default

`c2j test` should behave as if embedded mode is on. It should not require `--embed`, and it should not default to a remote SWF URL from `C2J_SWF_URL`.

Because recipe tests are not testing runtime persistence, use an isolated disposable embed root by default:

```text
<tmp>/c2j-test-embed-<run-id>/
```

The command should clean this directory up after the run unless a debug flag requests retention.

Recommended flags:

- `--runtime-root <path>`: use a specific embedded runtime root for debugging
- `--keep-runtime`: do not delete the temporary embedded runtime root

Avoid reusing the normal persistent embedded root:

```text
~/.c2j/embed/default
```

That root is useful for normal `submit` and `exec` workflows, but it makes test runs order-dependent and can leave state behind that changes later test results.

For passthrough modes, initialize dependencies similarly to `runjob.buildDeps` only when needed:

- call `c2jops.Register()` before running cases
- default `ops.ServiceDependencies2` can be empty
- add workflow control/database/SSE dependencies only if a test explicitly requires passthrough dependencies

This keeps normal mocked test cases fast and fully local.

## Implementation Phases

### Phase 1: Extract The Test Harness

1. Create `pkg/recipetest`.
2. Copy the reusable core from `/colony2/server/api/internal/recipetesting/service.go` into function-oriented harness files.
3. Replace Colony2 server imports:
   - `project.ID` -> `string`
   - old `recipesvc.Service` lookup -> local resolver function/interface
4. Keep the tests from:
   - `/colony2/server/api/internal/recipetesting/service_test.go`
   - handler tests that exercise core behavior, rewritten as direct harness tests
5. Verify with:

```bash
go test ./pkg/recipetest
```

Phase 1 is complete when an inline recipe case can validate and execute without any CLI code.

### Phase 2: Add `c2j test compile`

1. Add `cmd/c2j/internal/testjob`.
2. Port suite compilation from the old CLI.
3. Produce the same canonical IR shape:

```json
{
  "target_recipe": {},
  "cases": []
}
```

4. Add `cmd/c2j/internal/cmd/test.go`.
5. Register `newTestCmd()` in `cmd/c2j/internal/cmd/root.go`.
6. Add command tests covering:
   - `--recipe-file`
   - `--file`
   - `--stdin`
   - `scenario_md`
   - case filtering

Phase 2 is complete when `c2j test compile` can write canonical JSON locally.

### Phase 3: Add Local Validate And Run

1. Replace old `callRecipeTestEndpoint` with direct calls into `pkg/recipetest` package-level functions.
2. Preserve the old worker-pool scheduling behavior.
3. Emit live per-case status as each case completes.
4. Preserve local artifacts:
   - `summary.json`
   - `summary.md`
   - `cases/<case-id>/result.json`
   - `cases/<case-id>/artifacts/*`
   - `cases/<case-id>/evaluations.json`
5. Preserve `--jsonl-events`.
6. Add tests for:
   - parallel execution
   - `--fail-fast`
   - `--stop-on-failure`
   - artifact writing
   - status and exit-code mapping

Phase 3 is complete when `c2j test validate` and `c2j test run` pass using only inline recipes.

### Phase 4: Add c2j Recipe Selector Support

1. Extract current-cell target resolution from `submitjob`.
2. Support named recipes from the current cell.
3. Support git selectors.
4. Add tests with a temporary git repo containing `.c2j/recipes/default.yaml`.
5. Confirm `c2j submit --recipe default` and `c2j test run --recipe default` resolve the same recipe source.

Phase 4 is complete when tests can target recipes without `--recipe-file`.

### Phase 5: Finish Compatibility And Documentation

1. Update `/src/README.md` command summary and examples.
2. Document the test suite formats and local output directory.
3. Document that no server/API is required.
4. Decide whether to add a hidden compatibility alias later. If needed, add only a thin alias, not a second implementation.
5. Add a short migration note from old `c2 recipe test ...` to new `c2j test ...`.

## Exit Codes

Use `exitCoder` like `runjob` already does.

Recommended mapping:

- `0`: all selected cases valid or passed
- `1`: general runtime failure
- `2`: local compile or parse error
- `3`: local execution/harness error
- `4`: one or more cases invalid, failed, errored, or timed out
- `5`: invalid command options

The old network/API failure category should disappear because there is no local CLI-to-server network call.

## Test Coverage

Minimum coverage before considering the port complete:

- compile suite from YAML, JSON, markdown fenced block, and stdin
- reject invalid target argument combinations
- validate and run inline recipe cases
- validate and run current-cell named recipe cases
- validate and run explicit git selector cases
- run cases in parallel and print completions as they finish
- stop scheduling after first invalid or failed case when requested
- write summary and per-case output files
- inline artifacts decode to files correctly
- text-pattern evaluation can fail an enforced case
- report-only evaluation does not flip the case exit code
- duplicate mocks with the same matcher are consumed in declaration order
- op-case node scope is enforced
- passthrough dependency requirements fail clearly when dependencies are unavailable

Useful source tests to port or rewrite:

- `/colony2/cli/internal/cmd/recipe_testing_test.go`
- `/colony2/server/api/internal/recipetesting/service_test.go`
- `/colony2/server/api/internal/handlers/recipe_testing_test.go`

## Risks And Decisions

- The old CLI used untyped `map[string]interface{}` for suite IR. That is fast to port, but `pkg/recipetest` should expose typed structs so validation is not split across maps and hidden internals.
- The old core has a placeholder LLM judge. Keep it for behavioral compatibility in the first port, then decide whether to integrate a real provider later.
- The old core hardcodes `/tmp/recipe-tests/...` paths. Make the work root configurable so parallel local runs do not collide.
- The old `server_ref` name no longer describes local c2j behavior. Preserve wire compatibility internally if needed, but make command help say `name-or-selector`.
- The old compact YAML and scenario markdown compiler is minimal. Port it first, then improve it behind the same canonical IR contract.

## Done Criteria

The migration is done when:

1. `c2j test compile` produces canonical IR without contacting a server.
2. `c2j test validate` validates every selected case in-process.
3. `c2j test run` executes every selected case in-process and writes local result artifacts.
4. Inline recipe files, current-cell recipe names, and git selectors all work.
5. The old API handler path is not part of the new `c2j` test command execution path.
6. README documentation explains the local test loop.
7. `go test ./cmd/c2j/internal/testjob ./pkg/recipetest` passes.
