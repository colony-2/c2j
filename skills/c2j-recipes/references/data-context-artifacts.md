<!-- Sources: pkg/worker/test-fixtures/TEMPLATE_REFERENCE_CHEATSHEET.md; pkg/template/guides/JQ_JSON_TEMPLATE_GUIDE.md; pkg/template/cel_jq_spec.md; pkg/template/cel_artifact_sets_spec.md; pkg/worker/ops/op_executor.go; pkg/template/template_interpolate.go; README.md. -->

# Data, Context, Templates, And Artifacts

## Template Syntax

Use interpolation for mixed strings and single-expression mode for raw values:

```yaml
message: "Hello {{ inputs.name }}"
payload: "{{ sequence.fetch.outputs.body }}"
count: "{{ sequence.calc.outputs.total }}"
```

`when` clauses are pure CEL and do not use template markers:

```yaml
when: "inputs.retry_count < inputs.max_retries"
```

Use context prefixes. Do not write bare `outputs.foo`.

## Visible Context

Common references:

- `inputs.<name>`: submitted inputs or defaults.
- `sequence.<node>.outputs.<field>`: previous sibling node outputs.
- `sequence.<node>.artifacts`: previous sibling artifacts.
- `states.<state>.outputs.<field>`: completed state outputs.
- `states.<state>.artifacts`: completed state artifacts.
- `transition.from`, `transition.failure`, `transition.payload`: state transition data.
- `context.git.repo`, `context.git.ref`, `context.git.resolved_hash`, `context.git.author`: git context.
- `context.workflow.cell`, `context.workflow.job_id`: workflow context.
- `context.environment.worktree_path`, `context.environment.inbox`, `context.environment.outbox`: host-visible paths.
- `context.environment.op.worktree_path`, `context.environment.op.inbox`, `context.environment.op.outbox`: op-visible paths for supported ops.

Sequences can see prior siblings. State transitions can see the current state's sequence outputs during transition evaluation. Nested scopes inherit container inputs but cannot freely reach unrelated sibling internals.

## CEL Helpers

Useful built-ins include:

- `double(value)`, `int(value)`, `string(value)`, `bool(value)`
- `jq(value, expr)` and `value.jq(expr)`
- `json_stringify(value)`

`jq` returns `null` for no results, one native value for one result, and a list for multiple results.

```yaml
inputs:
  user_id: "{{ jq(inputs.payload, '.user.id') }}"
  tags: "{{ jq(inputs.payload, '.tags[]') }}"
  payload_json: "{{ json_stringify(inputs.payload) }}"
```

## Submitted Artifacts

Attach submitted artifacts with `c2j submit --artifact PATH` or `--artifact NAME=PATH`. Bind them into an op inbox explicitly:

```yaml
sequence:
  - id: inspect
    op: command_execution
    artifacts:
      brief.md: '${{ context.artifacts["brief.md"] }}'
    inputs:
      run: 'cat "${{ context.environment.op.inbox }}/brief.md"'
```

`command_execution` accepts artifact bindings. The worker materializes bound artifacts into the op inbox and collects files written to the op outbox as output artifacts.

## Artifact Outputs

Reference artifacts by name:

```yaml
sequence:
  - id: emit
    op: test_emit_artifact
  - id: consume
    op: test_consume_artifact
    inputs:
      artifact: '${{ sequence.emit.artifacts["foo"] }}'
outputs:
  consumed: "{{ sequence.consume.outputs.name }}"
```

Artifact keys expose fields such as `jobId`, `taskOrdinal`, `name`, and `sizeBytes` in CEL.

## Op-Visible Paths

Use `context.environment.op.*` in op inputs when the operation supports path transformation. `command_execution` and selector-backed `extension_execution` support op-visible path mapping. If an op does not support it, runtime raises an op-visible path resolution error.

For shell commands:

```yaml
inputs:
  working_directory: "{{ context.environment.op.worktree_path }}"
  run: |
    printf 'result' > "${{ context.environment.op.outbox }}/result.txt"
```

Avoid hard-coding host paths inside sandboxed ops. Use the context path visible to the op.
