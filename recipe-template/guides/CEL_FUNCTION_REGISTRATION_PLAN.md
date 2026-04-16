# CEL Function Registration Plan

Goal: register CEL functions at system setup time instead of hard‑coding them into the core `recipe-template` binary, focusing on compiled Go functions (CLI-backed extensions can be revisited later) and preserving static type validation for returned objects.

## Requirements
- Add new Go-defined functions through a small, centralized API.
- Provide safe execution defaults for added functions.
- Keep current behavior as default; configuration should be opt-in/overridable.
- Let api testserver and cortex servers add Go functions programmatically (no YAML), avoiding global state or stuffing data into `context.Context`.
- Support functions that return complex objects (maps/lists/custom structs) and ensure they flow through CEL via the adapter and static typing.
- Outputs must be declared so CEL type-checking catches invalid field access at compile time.

## Proposed Structure
- New package `pkg/funcregistry` that exposes:
  - `BuiltinRegistry`: map of `name -> cel.EnvOption` (or helper type) for compiled functions.
  - `TypeDecls`: slice of type descriptors derived from registered functions' inputs/outputs for env construction.
  - `EnvOptions()` helper that returns `[]cel.EnvOption` for env creation (built alongside type registrations).
- No YAML/config layer for now; configuration is code-driven via builders.

## Registration Flow
1) Server/test harness builds a `funcregistry.Builder` (non-global).
2) Builder starts with defaults (current jq/json_* functions) via `WithDefaults()`.
3) Call typed helpers (see below) to add Go-defined functions; builder records both env options and required type declarations.
4) Pass `builder.EnvOptions` to `ResolutionOptions` (and through execution/validation) to build the CEL env; the same builder exposes `TypeDeclarations()` to be fed into `cel.NewEnv` so returned structs are known to the type checker.

## Adding Builtin Go Functions (typed, closed API)
- Provide a typed builder API similar to `ops.NewStep` / `RegisterableOp`:
  - `NewUnaryFunc[In any, Out any](name string, impl func(In) (Out, error))`
  - `NewUnaryFuncCtx[In any, Out any](name string, impl func(context.Context, In) (Out, error))`
  - `NewBinaryFunc[A, B, Out any](...)`, etc.
  - Each helper infers input/output Go types via generics, registers those types with the CEL environment (`cel.Types` or `ext.NativeTypes`), and produces a `cel.Function` with precise input/output `*cel.Type` (not `Dyn`).
- Helpers take the CEL `types.Adapter` so implementations can convert complex native Go values (`map[string]any`, slices, structs registered via `ext.NativeTypes`) using `adapter.NativeToValue` before returning.
- For structs, JSON tags drive field names; we surface a CEL object type (e.g., `cel.ObjectType("pkg.MyStruct")`) so `result.foo` type-checks and `result.bar` fails at compile time.
- Register in `BuiltinRegistry` via `func init()` or explicit `RegisterBuiltin(name, builder)`.
- Document signature and sample usage; optionally generate docs.
- Programmatic injection path (no YAML):
  - Introduce `funcregistry.Builder` (non-global) that holds builtin specs and produces `[]cel.EnvOption`.
  - Servers create a builder during setup (`NewBuilder()`), call `WithDefaults()` to enable existing functions, then `WithBuiltin(name, option)` for extra Go functions, and finally use `EnvOptions()`.
  - The builder is passed as a constructor argument into the component that builds the CEL env (e.g., `NewEngine(builder EnvOptionProvider)`), keeping dependencies explicit and testable.
  - api testserver can create a fresh builder per test, add test-only functions, and avoid cross-test leakage; cortex server can wire its own additions similarly.

## Wiring Into Current Code Paths (no globals)

### recipe-template
- Update `pkg/template/resolution_options.go` to add `CELOptionsProvider func(adapter types.Adapter) ([]cel.EnvOption, error)` (or interface) on `ResolutionOptions`.
- In `NewRecipeResolutionContext` (`pkg/template/template_resolver.go`):
  - Build base env as today.
  - If `CELOptionsProvider` is set, call it with the adapter and append returned options before `env.Extend(...)`; functions can use the provided adapter to emit complex return types safely.
  - Also apply `builder.TypeDeclarations()` (e.g., `cel.Types(...)` or `ext.NativeTypes(...)`) when constructing the env so outputs have static field typing.
  - Default provider uses `funcregistry.DefaultBuilder().WithDefaults()` to keep existing jq/json_* behavior.

### recipe-worker compiler
- Extend `compiler.ExecutionOptions` with `CELOptionsProvider` (or `CELBuilder *funcregistry.Builder`).
- Modify `resolutionOptionsFromExecution` to thread this through to `template.ResolutionOptions`.
- `ExecuteRecipe` / `ExecuteNode` already pass `ResolutionOptions`; no global variables needed.
- Validation flow can now type-check expressions against declared return structs (e.g., `fn().bar` errors if `bar` not a field), matching the RegisterableOp type-safety precedent.

### server/api testserver
- File: `../api/cmd/testserver/main.go`.
  - After `serverdepsops.RegisterOps()` add:
    - `celFns := funcregistry.NewBuilder().WithDefaults()` (enables current functions).
    - Optionally attach testserver-only Go functions: `celFns.WithBuiltin("my_fn", myEnvOption)`.
  - Pass builder into recipe validation: update `recipesvc.NewRecipeWorkerCELValidator(...)` to accept a `CELOptionsProvider` param; wire it from `celFns`.
  - For runtime execution, ensure the workflow engine (via `serverdeps.NewEngineSetup` → `recipe-worker`) receives `ExecutionOptions{CELOptionsProvider: celFns.EnvOptions}` when executing recipes.

### cortex server
- File: `../cortex/internal/setup/setup.go`.
  - Create the same builder near recipe service creation: `celFns := funcregistry.NewBuilder().WithDefaults()`.
  - If cortex wants CLI-flag-driven additions, thread a slice of function names/binaries through `Config` → setup to mutate the builder before use.
  - Pass builder into `recipesvc.NewRecipeWorkerCELValidator(...)` (constructor gains optional provider).
  - Ensure any recipe execution path (e.g., `execute` subcommand) passes `ExecutionOptions{CELOptionsProvider: celFns.EnvOptions}` to the compiler so the same functions are available at runtime.

### Why no globals/context stuffing
- Builders are allocated in the server setup functions and handed down explicitly via constructor args and `ExecutionOptions`; nothing is stored in `init()` or package-level vars beyond default registry definitions.
- `context.Context` stays for cancellation/values unrelated to configuration; CEL options flow through typed parameters only.

## Backward Compatibility & Defaults
- `WithDefaults()` registers current functions (`json_stringify`, `json_parse`, `jq`, string JSON overloads) so behavior stays identical unless explicitly changed.

## Testing Strategy
- Unit: builder merge rules, duplicate/missing names, helper constructors (e.g., unary/binary), adapter interactions, and type declarations emitted for structs.
- Integration: build CEL env with a typed function returning a struct; verify `result.foo` type-checks and `result.bar` fails during validation.
- Backward compat: test `WithDefaults()` yields identical behavior for jq/json_* functions as today.
- Docs test: script to regenerate `docs/functions.md` from `BuiltinRegistry` and ensure it is up-to-date (CI check).

## Migration Steps
- Move existing hardcoded CEL functions into `funcregistry.BuiltinRegistry`.
- Wire `ResolutionOptions` and compiler `ExecutionOptions` to accept `CELOptionsProvider`.
- Update api testserver and cortex setup to create a builder, call `WithDefaults()`, optionally add functions, and pass it through validation/execution.
- Add documentation (`docs/functions.md` or update `guides`).

## Future Extensions
- Optional CLI-backed extension functions (reintroduce via builder once needed).
- Metrics per function (latency, errors) and optional circuit-breaker.
- Streaming / async functions if required by future ops.
