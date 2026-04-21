# Selector-Backed CEL Extension Functions Proposal

## Goal

Support extension functions that can be imported by selector, used inside CEL expressions, and remain consistent across replay and repeated runs.

This should complement the existing pieces:

- Go-defined CEL functions registered through `pkg/template/funcregistry`
- selector-backed extension ops resolved through `pkg/ops/extensions/selectors.go`

The new design should feel like extension ops from a recipe author's perspective, especially for `git+repo//path@ref` selectors.

## Current State

- CEL functions are injected programmatically through `template.CELOptionsProvider` and `funcregistry.Builder`.
- Extension ops already support local selectors (`./`, `../`) and git selectors (`git+...//path@ref`).
- Extension ops are shell/command backed and can read ambient state, mutate the workspace, and perform I/O.
- Validation mode already compiles CEL without executing functions, via `ModeValidate` in `pkg/template/template_resolver.go`.

The gap is selector-backed CEL functions. We can register functions in Go code today, but there is no extension packaging/import story comparable to extension ops.

## Recommendation

Use explicit recipe-level imports plus a trusted CLI execution backend.

The key choices are:

1. Import extension functions at the recipe level, not inline inside CEL expressions.
2. Reuse the extension-op selector grammar and same-repo resolution rules.
3. Resolve every imported function package to an exact commit before execution.
4. Let one manifest export many functions.
5. Treat purity as a contract and reduce accidental ambient inputs where practical.

The practical recommendation is:

- keep Go-registered functions for builtins and trusted server-side helpers
- add selector-backed extension function packages for recipe-defined imports
- make selector-backed extension functions CLI-backed in v1, with package-level defaults and a per-function `execution` line in `functions.yaml`

This does not hard-enforce purity. It matches the current trust model for extension ops: users are responsible for writing functions that behave like pure functions.

## Why Recipe Imports Instead Of Inline Selectors

Do not make CEL call sites resolve selectors directly, for example:

```cel
ext("git+https://...//tools/cel/text-utils@main", "slugify", inputs.title)
```

That has four problems:

- CEL compile-time validation would not know the function signatures ahead of time.
- The same selector would be resolved repeatedly.
- Floating refs could be re-resolved differently on replay.
- Function-name collisions and import shaping would become hard to reason about.

Instead, add explicit imports once at the recipe root and bind the selected functions into the CEL environment before expressions are compiled.

## Proposed Recipe Syntax

Add a top-level recipe metadata section:

```yaml
version: "1"

extensions:
  functions:
    - selector: git+https://github.com/acme/c2-extensions.git//tools/cel/text-utils@v1.2.3
      include: [slugify, semver_compare]
      rename:
        slugify: text_slugify
    - selector: ./tools/cel/date-utils

sequence:
  - name: choose_branch
    when: "semver_compare(inputs.current, inputs.target) < 0"
    inputs:
      branch: "${{ text_slugify(inputs.title) }}"
```

Rules:

- each import points at a function package, not an individual function
- if `include` is omitted, import all exported functions from the package
- `rename` is optional and remaps exported names into CEL-visible names
- name collisions after rename should fail recipe load
- selectors follow the same syntax as extension ops
- local selectors are resolved relative to the recipe source repo/worktree
- same-repo selectors should piggyback on the existing `RepositorySource` and `RepositoryRef` flow already used for extension ops

Keep imports recipe-scoped, not node-scoped. The function set should be stable for the entire recipe so CEL compilation and replay stay predictable.

## Proposed Extension Layout

Function packages should live wherever the selector points. There is no required root directory.

One possible layout is:

```text
tools/
  cel/
    text-utils/
      functions.yaml
      slugify.py
      semver_compare.py
```

Suggested selector form:

```text
git+https://github.com/acme/c2-extensions.git//tools/cel/text-utils@<ref>
```

A selector resolves one package directory, wherever that directory lives in the repo. That package can expose many functions.

## Manifest

Use a dedicated manifest instead of overloading `op.yaml`.

Example:

```yaml
name: text_utils
description: Shared text and version helpers
version: 1.0.0

shell: bash
timeout: 100ms
env:
  PYTHONUNBUFFERED: "1"

functions:
  - name: slugify
    mode: function
    execution: python3 slugify.py
    description: Convert text into a lowercase dash-separated slug
    args:
      - name: input
        schema:
          type: string
    returns:
      schema:
        type: string

  - name: semver_compare
    mode: json
    execution: python3 semver_compare.py
    description: Compare two semantic versions
    args:
      - name: left
        schema:
          type: string
      - name: right
        schema:
          type: string
    returns:
      schema:
        type: integer
```

Phase 1 should keep this intentionally narrow:

- one function package per directory
- many exported functions per manifest
- one `execution` line per function
- free function call syntax only, not member overloads
- schema types limited to primitives, lists, and `map[string]T`
- package-level `shell`, `working_directory`, `env`, and `timeout` act as defaults for all functions in the package

If we need overloads or deeper per-function runtime customization later, add them after the package import model is stable.

## Execution Backend

Selector-backed CEL functions should be CLI-backed in v1.

The implementation should reuse as much of the extension-op process launcher as possible, especially the lower-level execution helpers in `pkg/ops/extensions/runtime.go`, but with a dedicated manifest and invocation contract for function packages.

Recommended package-level runtime fields:

- `shell`
- `working_directory`
- `env`
- `timeout`

Recommended per-function runtime fields:

- `execution`
- `mode`

Recommended invocation modes:

### `mode: json`

Use this for multi-argument functions or structured input/output.

- host sends stdin JSON: `{"args":[...]}`
- process writes stdout JSON: `{"result": ...}`
- any stderr output is treated as diagnostic text and included in errors

### `mode: function`

Use this for the ergonomic case the user asked for: single arg in, single result out, no JSON envelope.

- exactly one declared arg
- host sends that arg directly on stdin, encoded as a plain schema-aware scalar value
- process writes the return value directly on stdout
- host parses stdout according to the declared return schema

In v1, `mode: function` should be limited to scalar types where plain text parsing is unambiguous:

- `string`
- `integer`
- `number`
- `boolean`

Recommended defaults:

- default working directory: the resolved function package directory
- no implicit task context
- no implicit git context
- no recipe/worktree env vars like `VIBETHIS_GIT_*`
- lean inherited environment where practical, ideally `PATH`, `HOME`, `TMPDIR` plus manifest `env`

Phase 1 can simply launch a fresh process per invocation. Memoization will avoid a lot of repeated work inside a run. If startup cost becomes a real problem later, we can add an optional daemon protocol separately.

Simple ABI summary:

- `json` mode input: `{"args":[...]}`
- `json` mode output: `{"result": ...}`
- `function` mode input: raw single argument on stdin
- `function` mode output: raw single result on stdout

The important part is that the host contract stays narrow even if the implementation is just a normal CLI.

## Purity Model

For v1, selector-backed CEL functions are trusted code.

That means:

- purity is an author contract, not something the runtime can fully prove
- authors are expected to avoid side effects and avoid depending on ambient state
- if a function reads the clock, filesystem, network, or arbitrary env, it may become nondeterministic

The system should still reduce accidental drift:

- pin selectors to exact commits
- avoid passing task/workflow/git context implicitly
- keep validation compile-only
- memoize repeated calls by input

This is the right compromise if we want git-ref-addressable functions now and users prefer shipping CLIs.

## Determinism Rules

Selector-backed CEL functions should be treated as pure-by-contract and the system should support that contract with light guardrails.

We should guarantee or strongly encourage all of the following:

1. Output depends only on explicit arguments.
2. The imported package code is pinned to exact content before execution.
3. The host does not inject recipe/task/git ambient inputs.
4. Validation never executes the function body.
5. Runtime can safely memoize repeated calls.

Concrete policy:

- resolve any branch or tag ref to a commit SHA once
- persist the resolved selector/commit with the loaded recipe
- use the resolved commit during execution and replay
- memoize by `resolved_selector + function_name + canonical_json(args)`
- document clearly that selector-backed CEL functions are trusted and may become nondeterministic if authors violate the purity contract

This is intentionally lighter than a sandboxed design. The main safety comes from commit pinning, narrow host inputs, and user discipline.

## Wiring Into The Existing Code

### 1. Recipe model

Extend `pkg/recipe/recipe.go`:

- add `Extensions` to `RecipeMetadata`
- add `Functions []ExtensionFunctionImport`

Suggested shape:

```go
type ExtensionImports struct {
    Functions []ExtensionFunctionImport `yaml:"functions,omitempty"`
}

type ExtensionFunctionImport struct {
    Selector string            `yaml:"selector"`
    Include  []string          `yaml:"include,omitempty"`
    Rename   map[string]string `yaml:"rename,omitempty"`
}
```

The loader should normalize each import into a concrete list of CEL-visible functions after applying `include` and `rename`.

### 2. Shared selector resolution

Extract or share the selector resolution logic from `pkg/ops/extensions/selectors.go`.

The selector grammar is already the right one:

- local selectors
- same-repo selectors using recipe source repo/ref
- `git+repo//path@ref`

Factor the path/ref resolution into a package shared by ops and function packages so the two systems do not drift.

### 3. Package loading and resolved import persistence

This part matters for replay.

When a root recipe is loaded from a selector, also resolve imported function package selectors and persist the result alongside the resolved recipe source. The runtime should not re-resolve floating refs later.

This likely means extending the root-source flow in `pkg/worker/compiler/root_source.go` so a loaded recipe carries, for each imported package:

- submitted selector
- resolved selector
- resolved commit
- package or manifest digest
- exported function names after import shaping

### 4. CEL provider integration

Build imported functions into a per-recipe CEL provider.

Suggested flow:

1. Start with `funcregistry.NewBuilder().WithDefaults()`
2. Resolve imported function packages
3. For each package, load the manifest and determine the exposed functions after `include` / `rename`
4. Register each exposed function into the builder with `WithBuiltin(...)`
5. Pass the composed provider through `ExecutionOptions` into `ResolutionOptions`

This keeps the current `template.CELOptionsProvider` design intact.

The builtin binding for a selector-backed function should:

- validate and canonicalize CEL args against the function signature
- check the memoization cache
- execute the function's configured `execution` line
- encode stdin according to the function mode
- validate and decode stdout according to the function mode and declared return schema
- convert the result back through the CEL adapter

### 5. Validation mode

Keep the current `ModeValidate` behavior:

- compile expressions against imported signatures
- do not execute imported function bodies

For validation placeholders, derive a zero value from the declared return schema, similar to how selector-backed ops already produce zero output from `output_schema`.

## Typing Strategy

There are two reasonable levels here:

### Phase 1

- strong typing for primitives, lists, and maps
- dynamic typing for complex object returns if needed

This is acceptable because the current `funcregistry` path is already fairly dynamic for structs.

### Future improvement

Add stronger object typing for schema-defined returns so invalid field access can fail during CEL compile rather than only at runtime.

That can come later. It should not block the package import architecture.

## Memoization

Because these functions are supposed to be pure, memoization is safe and useful.

Recommend a per-recipe-run cache keyed by:

- resolved selector or package digest
- function name after import shaping
- canonical JSON encoding of args

This improves performance and removes unnecessary repeated process launches inside the same workflow run.

## What Not To Do

- Do not make one selector resolve a single function only.
- Do not pass `TaskExecutionContext`, git state, or worktree metadata implicitly into selector-backed functions.
- Do not auto-discover function packages from conventional directories; imports should stay explicit.
- Do not claim the runtime enforces purity if it is just executing a CLI.

Explicit imports are the safer default.

## Incremental Plan

### Phase 1: plumbing

- add recipe-level function package imports
- add package selector resolution
- add a resolved-package manifest and cache
- inject imported functions into `CELOptionsProvider`

### Phase 2: CLI runtime

- implement CLI execution for imported packages using the shared process runtime
- add per-function `execution` support
- define both stdin/stdout contracts: `json` mode and single-arg `function` mode
- add runtime input/output schema validation
- add memoization

### Phase 3: validation and replay hardening

- persist resolved imports with root recipe resolution
- derive validation placeholders from function return schemas
- add replay tests proving branch/tag refs are pinned once
- add tests for include/rename behavior and collision handling

### Phase 4: richer typing

- add stronger CEL typing for object-shaped results
- optionally add overload support
- optionally add deeper per-function runtime overrides later if needed

## Testing Plan

- unit tests for selector parsing and same-repo resolution
- unit tests for package manifest loading and schema validation
- unit tests for import shaping: all-functions import, `include`, `rename`, collision errors
- unit tests for mode handling: `json` mode envelopes and single-arg `function` mode stdin/stdout parsing
- CEL integration tests showing imported functions are available in interpolation and pure-CEL mode
- validation tests proving imported functions compile but do not execute in `ModeValidate`
- replay tests proving a floating git ref is resolved once and then pinned
- determinism tests proving repeated execution with the same args returns the same value
- tests proving no task/git/worktree context is injected implicitly

## Bottom Line

The clean strategy is:

- import function packages explicitly at recipe load time
- resolve them using the same selector model as extension ops
- let one manifest export many functions
- give each function its own `execution` line and execution mode
- pin them to exact commits before execution
- execute them as trusted CLIs with either JSON mode or simple single-arg function mode
- inject the exposed functions into CEL through the existing `CELOptionsProvider` path

That gives us the extension-op ergonomics the recipe author wants, while keeping replay consistency as a documented contract backed by commit pinning, compile-time validation, and memoization.
