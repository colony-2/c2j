# Proposal: Artifact Set Inbox Bindings

Status: implemented

## Summary

Add directory-style artifact set bindings to op `artifacts:` maps.

Recipe authors should be able to bind a whole artifact set into an op inbox
without knowing artifact names ahead of time:

```yaml
sequence:
  - id: inspect
    op: command_execution
    artifacts:
      ./: '${{ context.artifacts }}'
    inputs:
      run: |
        find "${{ context.environment.op.inbox }}" -type f -maxdepth 3
```

The binding above materializes every artifact in `context.artifacts` at its
existing artifact name under the inbox root. A prefixed binding writes the same
set under a subdirectory:

```yaml
artifacts:
  submitted/: '${{ context.artifacts }}'
```

If `context.artifacts` contains `brief.md` and `docs/requirements.md`, the op
sees:

```text
${{ context.environment.op.inbox }}/submitted/brief.md
${{ context.environment.op.inbox }}/submitted/docs/requirements.md
```

This is the same shape as the user-suggested idea of:

```yaml
artifacts:
  dirname: '${{ context.artifacts }}'
```

The proposal recommends writing directory destinations with a trailing slash
(`dirname/`) for readability, but the implementation can accept both `dirname`
and `dirname/` when the value resolves to an artifact set.

## Existing Support Check

This does not appear to already exist as an op inbox binding feature.

Existing pieces that are already present:

- Submitted artifacts are exposed as `context.artifacts`.
- Step and state artifacts are exposed as `sequence.<node>.artifacts` and
  `states.<state>.artifacts`.
- CEL helpers such as `artifact_set`, `artifact_concat`, `artifact_filter`,
  `artifact_names`, and `artifact_unique` can build artifact collections.
- Op inbox bindings already accept explicit map entries:

  ```yaml
  artifacts:
    brief.md: '${{ context.artifacts["brief.md"] }}'
  ```

The missing piece is expansion. `pkg/worker/compiler/artifact_bindings.go`
currently resolves the `artifacts:` map and then coerces each value to one
artifact ref. A value that resolves to a map or list of artifact refs is not
expanded into multiple inbox bindings.

## Goals

1. Bind every artifact from an artifact set into an op inbox without listing
   names statically.
2. Work with `context.artifacts`, `sequence.<node>.artifacts`,
   `states.<state>.artifacts`, child recipe output artifacts, and CEL helper
   results.
3. Preserve each artifact's existing name as the default inbox-relative path.
4. Let authors put a whole set under an inbox prefix.
5. Let explicit bindings and artifact set bindings coexist in one op.
6. Keep artifact content out of template evaluation. Template resolution should
   operate only on artifact refs.
7. Fail deterministically on destination collisions.

## Non-Goals

- Do not auto-inject submitted artifacts into every op.
- Do not read, download, or embed artifact contents during template evaluation.
- Do not add globbing or artifact discovery in this feature.
- Do not require authors to use CEL helpers for the common pass-through case.

## Proposed Recipe Syntax

Use the existing `artifacts:` map. The map key remains the inbox destination,
and the resolved value determines whether the binding is explicit or expanded.

### Bind A Set At Inbox Root

```yaml
artifacts:
  ./: '${{ context.artifacts }}'
```

`./` means "use each artifact's own name relative to the inbox root."

### Bind A Set Under A Directory

```yaml
artifacts:
  submitted/: '${{ context.artifacts }}'
  requirements-input/: '${{ states.requirements.artifacts }}'
```

Each artifact is written to:

```text
<inbox>/<directory>/<artifact name>
```

### Combine Set And Explicit Bindings

```yaml
artifacts:
  submitted/: '${{ context.artifacts }}'
  canonical-brief.md: '${{ context.artifacts["brief.md"] }}'
  previous/: '${{ sequence.prepare.artifacts }}'
```

Explicit bindings keep their current behavior. Artifact set bindings expand
alongside them.

### Filter Or Combine Sets

The existing CEL artifact helpers become useful without inventing another
binding language:

```yaml
artifacts:
  release/: '${{ artifact_filter(sequence.build.artifacts, {"name_suffix": ".zip"}) }}'
  all-inputs/: '${{ artifact_unique(artifact_concat(context.artifacts, states.plan.artifacts)) }}'
```

## Semantics

For each entry under `artifacts:`:

1. Resolve templates exactly as today.
2. If the value is a single artifact ref, preserve current behavior:

   ```yaml
   artifacts:
     brief.md: '${{ context.artifacts["brief.md"] }}'
   ```

3. If the value is an artifact set, expand it:

   ```yaml
   artifacts:
     submitted/: '${{ context.artifacts }}'
   ```

4. For every artifact ref in the set, compute:

   ```text
   destination = clean_join(binding key, artifactRef.NameValue())
   ```

5. Validate the final destination with the same path safety rules as explicit
   artifact binding names.
6. Add `destination -> artifactRef` to the op invocation's artifact binding map.
7. If any destination already exists, fail with a clear error.

Accepted artifact set values should include:

- `map<string, artifacts.Ref>`
- `map<string, swf.ArtifactKey>`
- `[]artifacts.Ref`
- `[]swf.ArtifactKey`
- CEL list/map wrappers that normalize to those native values
- `null`, which expands to an empty set

For maps, sort keys before expansion so diagnostics are deterministic. The
destination path should still come from `artifactRef.NameValue()`, not from map
iteration order.

## Collision Rules

Default behavior should be fail-fast.

Examples that should fail:

```yaml
artifacts:
  ./: '${{ context.artifacts }}'
  brief.md: '${{ states.generated.artifacts["brief.md"] }}'
```

If `context.artifacts` already contains `brief.md`, the compiler should fail
with an error like:

```text
artifact binding destination "brief.md" is defined by both "./" expansion and explicit binding "brief.md"
```

The first implementation should not add `replace`, `skip`, or `rename`
policies. Authors can avoid collisions by binding sets under prefixes or by
using `artifact_filter` / `artifact_unique` before binding.

Also fail on file/directory conflicts such as:

```yaml
artifacts:
  docs: '${{ context.artifacts["docs"] }}'
  docs/: '${{ states.plan.artifacts }}'
```

If one binding writes `docs` as a file and another writes `docs/<name>`, the
result depends on filesystem materialization order. The compiler should detect
and reject that shape.

## Why This Shape

This keeps the existing recipe parser shape:

```go
Artifacts map[string]interface{} `yaml:"artifacts,omitempty"`
```

That matters because allowing this:

```yaml
artifacts: '${{ context.artifacts }}'
```

would require changing `NodeMetadata.Artifacts` from a map to a union-like
value and updating parser/schema logic. It is attractive for the root-inbox
case, but it makes the mixed case less obvious:

```yaml
artifacts: ...
```

versus:

```yaml
artifacts:
  submitted/: ...
  brief.md: ...
```

Directory-style bindings keep one form for both simple and mixed usage.

This also avoids a reserved wildcard key such as `"**"`. A key like `"**"` is
less connected to the existing mental model that `artifacts:` keys are inbox
destinations. With this proposal, the destination key still means "where the
artifact files appear."

## Implementation Plan

1. Keep `recipe.NodeMetadata.Artifacts` as `map[string]interface{}`.
2. Extend `pkg/worker/compiler/artifact_bindings.go`:
   - Resolve the raw map with `resCtx.ResolveMap`.
   - Try `coerceArtifactRef(value)` first to preserve explicit behavior.
   - If that fails, try `coerceArtifactSet(value)`.
   - Expand set entries into final destination paths.
   - Track source descriptions for collision errors.
3. Add helpers:
   - `coerceArtifactSet(value interface{}) ([]artifacts.Ref, error)`
   - `artifactBindingPrefix(key string) (string, error)`
   - `joinArtifactBindingPath(prefix, artifactName string) (string, error)`
   - `detectArtifactBindingCollisions(bindings map[string]artifacts.Ref) error`
4. Reuse the existing path validation rules used by materialization. If those
   rules only live in `pkg/worker/ops`, move the pure path validation helper to
   a small shared package to avoid duplicating behavior.
5. Sort expansion inputs for deterministic errors.
6. Leave `pkg/worker/ops/op_executor.go` materialization mostly unchanged. It
   should receive the already-expanded binding map.

## Tests

Add compiler-level tests:

- `context.artifacts` bound at `./` expands to each submitted artifact name.
- `context.artifacts` bound at `submitted/` writes under that prefix.
- `sequence.prepare.artifacts` can be expanded into a later op.
- `states.plan.artifacts` can be expanded into a later state op.
- Explicit and expanded bindings can be combined.
- Duplicate final destinations fail with a clear error.
- File/directory destination conflicts fail deterministically.
- `artifact_filter(...)` returning a list can be expanded.
- Existing explicit single-artifact bindings still pass unchanged.

Add at least one recipe fixture that runs `command_execution` and verifies files
exist at `context.environment.op.inbox` with the expected names.

## Documentation Update

Update `C2J_SUBMIT_ARTIFACTS_USER_GUIDE.md` with a short section:

```yaml
artifacts:
  ./: '${{ context.artifacts }}'
```

and:

```yaml
artifacts:
  submitted/: '${{ context.artifacts }}'
```

The guide should explicitly say that submitted artifacts are still opt-in per
op; wildcard bindings are not automatic injection.

## Open Questions

1. Should we later add scalar root binding syntax:

   ```yaml
   artifacts: '${{ context.artifacts }}'
   ```

   This is convenient but requires parser/schema changes.
2. Should `artifact_unique(..., "name")` become the documented way to handle
   intentional duplicate names before binding, or should binding support an
   explicit collision policy later?
