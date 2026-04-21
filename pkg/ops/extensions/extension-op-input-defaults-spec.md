# Extension Op Input Defaults Specification

## Overview

Extension ops should support input defaults in the same practical way built-in Go ops do today:

- omitted fields can be populated automatically
- string defaults can contain normal template or CEL expressions
- the final payload that reaches the extension process remains schema-valid

The extension-op schema format already uses JSON Schema-shaped `input_schema` data. The right author-facing syntax is to adopt the standard `default` keyword at the schema node where the default belongs, matching OpenAPI and JSON Schema conventions, rather than inventing a new field.

This spec covers selector-resolved extension ops loaded through `pkg/ops/extensions/selectors.go`.

## Goals

- Add first-class default values to extension-op `input_schema`.
- Use standard schema syntax: `default`.
- Allow string defaults to flow through the normal template/CEL expansion path.
- Allow `required` plus `default` on the same property.
- Keep behavior aligned with existing built-in op defaults where practical.
- Share one default-materialization implementation across selector-resolved extension ops.

## Non-Goals

- Do not add defaults to `output_schema`.
- Do not introduce a second extension-only keyword such as `x-default`.
- Do not attempt to execute arbitrary schema combinators to synthesize defaults in v1.
- Do not change the existing template scope rules for op inputs.

## Current State

Today extension ops load and validate `input_schema`, but they do not materialize defaults.

- selector-backed ops skip `workerops.InjectDefaults(...)` entirely in the compiler path
- validation happens against the compiled schema, which means missing required fields fail even if the schema author wrote a `default`

As a result, extension ops currently cannot express:

- static defaults such as `default: main`
- CEL-backed defaults such as `default: "${{ context.git.base_ref }}"`
- nested object defaults that create omitted containers

## Proposed Authoring Syntax

Use the existing `input_schema` field and allow `default` anywhere a property or subschema default belongs.

Example `op.yaml`:

```yaml
name: repo_info
run: python3 main.py

input_schema:
  type: object
  required: [repo]
  properties:
    repo:
      type: string
    ref:
      type: string
      default: main
    summary:
      type: string
      default: "${{ context.git.base_ref }}"
    options:
      type: object
      properties:
        dry_run:
          type: boolean
          default: true
        labels:
          type: array
          items:
            type: string
          default: ["triage"]
```

Recipe usage:

```yaml
- id: inspect_repo
  op: ./tools/ops/repo-info
  inputs:
    repo: github.com/acme/service
```

Runtime payload after defaulting and interpolation:

```yaml
repo: github.com/acme/service
ref: main
summary: <resolved from normal template/CEL expansion>
options:
  dry_run: true
  labels:
    - triage
```

## Semantics

### Source Of Truth

- `input_schema` remains the only schema source for extension-op inputs.
- Defaults come from the schema's standard `default` keyword.
- No new top-level `defaults` block is introduced.

### Precedence

1. Explicit user input wins.
2. Schema defaults fill only missing fields.
3. A key that is present with `null`, `false`, `0`, `""`, `[]`, or `{}` is considered explicitly set and is not overwritten.

This matches the current built-in default behavior, which only fills absent keys.

### Template And CEL Expansion

Defaults must be injected before `ResolutionContext.ResolveMap(...)` runs.

That gives schema defaults the same behavior as normal user-authored input values:

- plain scalars remain plain scalars
- string defaults may use `{{ ... }}` and `${{ ... }}`
- nested string values inside object or array defaults are resolved recursively

No new template scope is introduced. Defaults see the same resolution context that normal op inputs already see at that point in execution.

### Required Plus Default

Extension ops should allow a schema property to be both:

- listed in `required`
- supplied by `default`

Operationally, this means:

- omission is allowed at recipe authoring time when a default exists
- the materialized payload must still satisfy the full schema before execution

This is intentionally more practical than strict raw-instance JSON Schema validation, and matches the built-in-op authoring model better.

### Nested Objects

If a missing object property has descendant defaults, the object container should be created automatically.

Example:

```yaml
input_schema:
  type: object
  properties:
    config:
      type: object
      properties:
        host:
          type: string
          default: localhost
        port:
          type: integer
          default: 5432
```

Missing `config` should materialize as:

```yaml
config:
  host: localhost
  port: 5432
```

If an object schema also has its own `default`, that object default is copied first, then nested property defaults fill any still-missing descendants.

### Arrays

For v1:

- if an array property itself has `default`, deep-copy that array when the field is missing
- if an array is present and `items` is an object schema, apply nested defaults to each existing element
- item defaults do not create new array elements when the array is absent and the array schema itself has no default

### Unsupported Schema Shapes

To keep behavior predictable in v1, default materialization should support deterministic schema locations only:

- root schema
- object `properties`
- array `items` for existing elements

Defaults declared under `oneOf`, `anyOf`, `allOf`, `not`, or conditional branches should be rejected at extension-load time if the implementation cannot apply them deterministically.

Failing fast is better than silently ignoring a default the author expected to run.

### Reserved Fields

Selector-backed extension execution reserves out-of-band fields such as `sandbox`. Those are not part of the extension `input_schema` defaulting contract and should remain excluded from payload defaulting.

## Execution Order

The extension-op input pipeline should become:

1. Start with the raw authored input map.
2. Materialize schema defaults into the raw map.
3. Resolve templates/CEL over the resulting map.
4. Validate the resolved payload against the compiled input schema.
5. Normalize and dispatch the execution payload.

This order matters:

- step 2 before step 3 is what allows CEL-backed defaults to expand
- step 4 after step 3 ensures the executed payload is type-correct
- explicit inputs still override defaults because missing fields are the only insertion points

## Implementation Design

### Shared Default Materializer

Add a shared helper in `pkg/ops/extensions` that:

- walks an extension `input_schema`
- deep-copies schema defaults into a `map[string]interface{}`
- recursively creates omitted object containers when needed
- returns whether anything was injected

Suggested shape:

```go
func ApplySchemaDefaults(schema map[string]any, input map[string]interface{}) (bool, error)
```

Or, if we want the schema parsed once and reused:

```go
type InputDefaults struct { ... }

func BuildInputDefaults(schema map[string]any) (*InputDefaults, error)
func (d *InputDefaults) Apply(input map[string]interface{}) (bool, error)
```

The parsed form is preferable because selector-resolved ops already parse `input_schema` during load.

The materializer should:

- deep-copy default values before inserting them
- recurse into nested objects and arrays
- avoid mutating shared schema data
- reject unsupported default locations during build/load

### Load-Time Validation

When an extension op is loaded, the implementation should validate that every declared default is locally schema-valid.

Examples that should fail fast:

- `type: integer` with `default: nope`
- `type: array` with `default: {bad: shape}`
- unsupported combinator-based default placement

This keeps extension author errors out of runtime execution paths.

### Selector-Backed Ops

Selector-backed ops already resolve the concrete extension package before input resolution in the compiler path. That is the correct place to apply defaults.

Required change in compiler flow:

- after `loadSelectorOp(...)`
- before `resCtx.ResolveMap(metadata.Inputs)`
- apply defaults from the resolved extension op's `input_schema`

This ensures selector-backed defaults:

- participate in normal template/CEL expansion
- satisfy `required` checks before schema validation
- behave the same way on replay because the selector is already pinned

Recommended API on the resolved selector object:

```go
func (r *ResolvedOp) ApplyInvocationDefaults(input map[string]interface{}) (bool, error)
```

### Parse-Time Validation

Selector-backed ops currently skip parse-time input validation because their concrete schema is not available in static recipe parsing. That should remain unchanged.

### Schema Generation

No new schema-generation keyword is needed.

Selector-backed ops remain generic in the static recipe schema because their concrete `input_schema` is only known once the selector is resolved.

## Testing Strategy

- Unit:
  - apply scalar defaults to missing properties
  - apply nested object defaults and create omitted containers
  - apply array defaults and recurse into existing object items
  - preserve explicit values, including explicit `null`
  - resolve nested string defaults through normal template/CEL expansion
  - reject invalid default values at extension-load time
  - reject unsupported combinator-based defaults in v1
- Compiler / integration:
  - selector-backed op with `default: main`
  - selector-backed op with `default: "${{ context.git.base_ref }}"`
  - replay/validation mode keeps the same materialized payload shape

## Backwards Compatibility

This is additive.

- existing extension ops without `default` remain unchanged
- existing built-in ops keep their struct-tag default behavior
- recipes that already pass every input explicitly continue to work

The only behavioral change is that omitted extension-op inputs may now become valid when the schema supplies defaults.

## Recommendation

Adopt standard schema `default` support for extension-op `input_schema`, with default materialization happening before template resolution and before final schema validation.

That gives extension ops the missing behavior users expect while keeping the author-facing syntax boring and familiar:

- same keyword as OpenAPI / JSON Schema
- same execution timing as built-in op defaults
- one shared implementation across selector-resolved extension ops
