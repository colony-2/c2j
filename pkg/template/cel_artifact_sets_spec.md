# CEL Custom Function Spec: Artifact Sets

## Goals
- Provide a small, composable set of CEL helpers for building and organizing artifact collections (concat/flatten, filter, etc.) in templates.
- Implement these helpers **outside** `recipe-template` by using the existing CEL function extension point, with the concrete registration living in `/src/server/cortex`.

## Non-Goals
- No new “cell listing” / topology functions (not part of this spec).
- No artifact content I/O (no downloading, reading bytes, opening streams). Helpers operate on artifact metadata only.
- No changes to how step outputs store artifacts; helpers are utilities over the existing `sequence.*.artifacts` / `states.*.artifacts` values.

## Background / Data Model
- In templates, step artifacts are exposed as maps under:
  - `sequence.<step>.artifacts`
  - `states.<state>.artifacts`
  - `sequence.<step>.runs[i].artifacts` (when available)
- Artifact values are treated as `swf.ArtifactKey` in CEL (struct fields via JSON tags: `jobId`, `taskOrdinal`, `name`, `sizeBytes`).

## Extension Point (How These Functions Are Added)
`recipe-template` already supports injecting CEL types/functions via `template.ResolutionOptions.CELOptionsProvider`:
- `CELOptionsProvider.TypeOptions()` are appended before `cel.NewEnv(...)`
- `CELOptionsProvider.FunctionOptions(adapter)` (or `FunctionOptionsWithContext(adapter, ctxProvider)`) are added via `env.Extend(...)`

In `/src/server/cortex`, functions are registered by building a `funcregistry.Builder` and passing it as the engine’s `CELOptionsProvider` (see `/src/server/cortex/internal/setup/setup.go`).

## Proposed CEL Functions
All functions below are **function style** (no member overloads) to keep registration straightforward.

### 1) `artifact_set`
Normalize a mix of artifact containers into a single list of `swf.ArtifactKey`.

- Signature:
  - `artifact_set(...inputs) -> list<swf.ArtifactKey>`
- Accepted inputs (any mix; flattened left-to-right):
  - `swf.ArtifactKey` (single)
  - `list<swf.ArtifactKey>`
  - `map<string, swf.ArtifactKey>` (typical `*.artifacts` map)
  - `null` (ignored)
- Determinism:
  - When flattening maps, iterate keys in sorted order (ASC) to avoid nondeterministic output.
- Errors:
  - Unsupported input types raise a CEL error: `artifact_set: unsupported input type <type>`

### 2) `artifact_concat`
Alias for `artifact_set` for readability in templates.

- Signature:
  - `artifact_concat(...inputs) -> list<swf.ArtifactKey>`
- Behavior:
  - Exactly the same as `artifact_set`.

### 3) `artifact_filter`
Filter a list/set of artifacts by name “grep” and size ranges.

- Signature:
  - `artifact_filter(arts, opts) -> list<swf.ArtifactKey>`
- Parameters:
  - `arts`: any input accepted by `artifact_set` (the function normalizes internally).
  - `opts`: `map<string, dyn>` with optional keys:
    - `name_prefix` (`string`)
    - `name_suffix` (`string`)
    - `name_contains` (`string`) - substring match (case-sensitive).
    - `name_regex` (`string`, RE2) - grep-like pattern; compile failure is a CEL error.
    - `min_size` (`int`, bytes) - inclusive lower bound.
    - `max_size` (`int`, bytes) - inclusive upper bound.
- Semantics:
  - All provided predicates are ANDed together.
  - Size is read from `artifact.sizeBytes`.
  - Unknown sizes are represented as `-1`:
    - If `min_size` is provided and `min_size > -1`, unknown size fails the predicate.
    - If `max_size` is provided, unknown size passes unless `max_size < 0` (rare; generally callers should use `min_size` to require known sizes).
- Errors:
  - `artifact_filter: expected opts to be a map` (when `opts` is non-null and not a map)
  - `artifact_filter: invalid name_regex: <err>` (regex compile error)

### 4) `artifact_names`
Extract names from a list/set of artifacts.

- Signature:
  - `artifact_names(arts) -> list<string>`
- Behavior:
  - Equivalent to `artifact_set(arts).map(a, a.name)`, but implemented as a builtin for convenience.

### 5) `artifact_unique`
Deduplicate artifacts by `name` or by key identity.

- Signature:
  - `artifact_unique(arts, by="name|key") -> list<swf.ArtifactKey>`
- Parameters:
  - `by`:
    - `name` (default): keep first occurrence per `artifact.name`.
    - `key`: keep first occurrence per `(jobId, taskOrdinal, name)` tuple.
- Determinism:
  - Preserves first-seen order from the normalized input list.
- Errors:
  - `artifact_unique: invalid by: <value>`

## Behavior Notes
- All functions return empty lists (`[]`), not `null`, when there are no artifacts after normalization/filtering.
- Helpers must not do any artifact I/O; they only read fields already present on `swf.ArtifactKey`.

## Implementation Notes (Cortex)
Implement the functions as CEL env options in `/src/server/cortex` using the existing provider wiring:
- Extend the `celFns := funcregistry.NewBuilder().WithDefaults()` builder in `/src/server/cortex/internal/setup/setup.go` by adding new builtins via `celFns.WithBuiltin("artifact_set", factory)` etc.
- Prefer implementing these as `cel.Function` bindings that accept `dyn` inputs and use `ref.Val` / `traits.Mapper` / `traits.Lister` to normalize lists/maps without JSON round-tripping (to preserve `swf.ArtifactKey` values as native structs).
- Do **not** add any new code to `recipe-template` beyond what already exists for the extension point.

## Example Usage
```yaml
steps:
  - id: bundle
    op: pack
    inputs:
      release_artifacts: >-
        {{ artifact_filter(
             artifact_concat(sequence.build.artifacts, states.test.artifacts),
             {"name_regex": ".*\\.(tar\\.gz|zip)$", "min_size": 1024}
           )
        }}
      release_names: "{{ artifact_names(sequence.build.artifacts) }}"
      unique_by_name: "{{ artifact_unique(sequence.build.artifacts, 'name') }}"
```

## Testing
Add tests in `/src/server/cortex` (where the functions are registered) to ensure registration + behavior match real wiring:
- Unit tests (pure function behavior):
  - `artifact_set` flattens: single, list, map; ignores null; rejects unsupported input types.
  - Map key sorting behavior is deterministic.
  - `artifact_filter` supports:
    - prefix/suffix/contains
    - `name_regex` with both match + compile error paths
    - `min_size`/`max_size` inclusive ranges
    - unknown `sizeBytes=-1` behavior for min/max
  - `artifact_unique` by `name` and by `key`, preserving first occurrence.
- Integration test:
  - Build a `funcregistry.Builder` with defaults + these builtins, pass it through the same path cortex uses for template resolution/validation, and evaluate a CEL expression that uses `sequence.*.artifacts` to confirm the objects are `swf.ArtifactKey` and the helpers interoperate correctly.

## Rollout Notes
- This is additive: new CEL builtins only.
- Because registration lives in `/src/server/cortex`, other binaries that use `recipe-template` must explicitly opt in by providing the same `CELOptionsProvider` if they want these helpers available.
