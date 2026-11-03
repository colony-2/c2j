<!-- Sources: C2J_SKILLS_IMPLEMENTATION_PLAN.md; pkg/recipe/recipe.go; pkg/recipe/node.go; pkg/recipe/types.go; pkg/recipe/shared.go; pkg/worker/test-fixtures/recipes/*.yaml. The maintained /recipes/docs tree was not present in this checkout. -->

# Recipe Files

## Root Shapes

A recipe root must contain exactly one executable shape:

- `op`: a single operation.
- `sequence`: ordered nodes.
- `state`: a state machine.
- `child_group`: fan-out child recipe node.

Every recipe should also carry identity metadata:

```yaml
id: example
version: "1.0.0"
desc: Short purpose
input_schema: {}
sequence: []
outputs: {}
```

Supported root metadata includes `schema_version`, `version`, `id`, `desc`, `input_schema`, `defs`, and `extensions`.

## Inputs

Declare submitted inputs with `input_schema`. Set root `schema_version: 2` for
structured inputs; omission or version 1 keeps legacy behavior. The recipe
release `version` is independent. Common types are:

- `string`
- `number`
- `boolean`
- `artifact`
- `artifact_map`
- In v1, `object` is an immutable checkpoint.
- In v2, `checkpoint` is an immutable checkpoint, `object` is ordinary data, and
  `array` is a list. Optional `object_type` constrains a checkpoint contract.

V2 objects support `properties` and boolean-or-schema `additionalProperties`
(default true); arrays support `items`. Nested declarations use the same
`required` and `default_value` fields. Each include keeps its own schema version.
See [schema versions](../../../../GUIDE-Recipe-Schema-Versions.md) for migration,
default semantics, and replay behavior.

Use `required: true` for required values and `default_value` for defaults:

```yaml
input_schema:
  prompt:
    type: string
    required: true
  max_items:
    type: number
    default_value: 10
```

Submitted data may not contain keys absent from `input_schema`.

## Nodes

Nested nodes may use `op`, `sequence`, `state`, `child_group`, `shared`, or `include`.

```yaml
sequence:
  - id: collect
    op: git_file_collector
    inputs:
      context_dir: "{{ context.environment.worktree_path }}"
      file_patterns: ["**/*.go"]
  - id: summarize
    op: command_execution
    inputs:
      run: "printf '%s\n' 'collected {{ sequence.collect.outputs.file_count }} files'"
outputs:
  count: "{{ sequence.collect.outputs.file_count }}"
```

## Workspace Selection

All executable node shapes accept `workspace: {cell: cellB, ref: main}`. The ref is optional; both values can be templates. See [workspaces.md](workspaces.md) for inheritance, fresh-workspace boundaries, and input evaluation.

## Shared Nodes And Includes

Use `defs` for local reusable nodes and reference them with `shared`. Use `include` for recipe references discovered by the recipe provider.

```yaml
defs:
  echo:
    op: command_execution
    inputs:
      run: "echo {{ inputs.message }}"
sequence:
  - id: first
    shared: echo
  - id: imported
    include: other-recipe
```

Includes can be a string or object:

```yaml
include:
  recipe: other-recipe
```

## Operation Inputs

Use `inputs:` for op input fields. Current examples use `inputs`, not `with`.

```yaml
op: command_execution
inputs:
  run: "go test ./..."
  timeout: 30s
```

Known built-in ops validate input shape at parse/compile time unless a value is templated. Selector-backed extension ops are resolved at runtime.
