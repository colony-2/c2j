# Extension Op Input Defaults Specification

## Overview

Extension ops should support input defaults in the same practical way built-in Go ops do today:

- omitted fields can be populated automatically
- string defaults can contain normal template or CEL expressions
- the final payload that reaches the extension process remains schema-valid

The extension-op schema format already uses JSON Schema-shaped `input_schema` data. The right author-facing syntax is to adopt the standard `default` keyword at the schema node where the default belongs, matching OpenAPI and JSON Schema conventions, rather than inventing a new field.

This spec covers both extension-op execution paths:

- selector-backed extension ops resolved through `pkg/ops/extensions/selectors.go`
- legacy runtime-discovered extension ops loaded from `.colony2/ops/*/op.yaml`

## Goals

- Add first-class default values to extension-op `input_schema`.
- Use standard schema syntax: `default`.
- Allow string defaults to flow through the normal template/CEL expansion path.
- Allow `required` plus `default` on the same property.
- Keep behavior aligned with existing built-in op defaults where practical.
- Share one default-materialization implementation between selector-backed and discovered extension ops.

## Non-Goals

- Do not add defaults to `output_schema`.
- Do not introduce a second extension-only keyword such as `x-default`.
- Do not attempt to execute arbitrary schema combinators to synthesize defaults in v1.
- Do not change the existing template scope rules for op inputs.

## Current State

Today extension ops load and validate `input_schema`, but they do not materialize defaults.

- selector-backed ops skip `workerops.InjectDefaults(...)` entirely in the compiler path
- discovered extension ops use a dynamic wrapper input type, so struct-tag defaults do not apply
- both paths validate against the compiled schema, which means missing required fields fail even if the schema author wrote a `default`

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

The parsed form is preferable because selector-backed and discovered ops already parse `input_schema` during load.

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

### Discovered Extension Ops

Discovered extension ops need the same runtime behavior, but the compiler only sees a registered `RegisterableOp`.

Recommended approach:

- add an optional op-level interface for custom input default application
- let discovered extension ops implement it using the same shared schema-default helper
- keep the existing struct-tag `InjectDefaults(...)` path for built-in Go ops

Example optional interface:

```go
type InputDefaultsApplier interface {
    ApplyInputDefaults(input map[string]interface{}) (bool, error)
}
```

Compiler behavior:

1. If the registered op implements `InputDefaultsApplier`, call it.
2. Then run existing `workerops.InjectDefaults(...)` for struct-tag defaults.

This keeps built-in ops unchanged while giving extension ops a first-class schema-based default path.

### Parse-Time Validation

Discovered extension ops already do parse-time YAML validation through `extInputsWrapper.UnmarshalYAML(...)`.

That validation must be updated so missing required fields with defaults no longer fail early.

Recommended behavior:

- copy the authored input map
- apply schema defaults to the copy
- validate the copy against the compiled schema
- preserve the original authored input map for recipe storage

This keeps recipe authoring ergonomic without rewriting the recipe's explicit input payload.

Selector-backed ops currently skip parse-time input validation because their concrete schema is not available in static recipe parsing. That should remain unchanged.

### Schema Generation

No new schema-generation keyword is needed.

Discovered extension ops already expose their `input_schema` through the wrapper type used for schema reflection. Because `default` is already a standard schema annotation, generated documentation should carry it through automatically wherever the reflector preserves it.

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
  - discovered extension op allows omitted required+default fields during parse-time validation
  - discovered extension op executes with schema defaults materialized before template resolution
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
- one shared implementation across selector-backed and discovered extension ops
