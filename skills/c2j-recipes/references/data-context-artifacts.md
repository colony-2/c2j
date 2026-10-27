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
- `context.workflow.cell`, `context.workflow.job_id`: owning job context.
- `context.workspace.cell`, `context.workspace.scope_id`: active workspace identity; the cell defaults to the owning cell outside overrides. See [workspaces.md](workspaces.md).
- `context.environment.worktree_path`, `context.environment.inbox`, `context.environment.outbox`: host-visible paths.
- `context.environment.op.worktree_path`, `context.environment.op.inbox`, `context.environment.op.outbox`: op-visible paths for supported ops.

Sequences can see prior siblings. State transitions can see the current state's sequence outputs during transition evaluation. Nested scopes inherit container inputs but cannot freely reach unrelated sibling internals.

## Secret Inputs And Permissions

Use whole-value references in secret-enabled input fields. Explicit references
authored in the root recipe's repository need no `perms` grant. Op defaults from
that repository are also trusted. References in foreign recipes and defaults
from foreign ops require authorization; the target workspace does not determine
the trust root.

```yaml
id: separate-authorities
op: command_execution
perms:
  jobdb:
    secrets:
      resolve: [bar]
inputs:
  env:
    SERVICE_TOKEN: '${{ context.secrets.foo }}'
  run: ./worker
```

In a root-repo recipe, this supplies `foo` as an input while allowing only `bar`
through the delegated lease and secret helper. Input resolution never enlarges
the lease. A `jobdb` grant replaces the inherited delegated grant, independently
of input authorization.

On an include node, `perms.jobdb.secrets.resolve` authorizes the included recipe's
input references and bounds the grants it can request; it does not grant all
descendants runtime secret access. Descendants need explicit grants for lease
retrieval. Foreign includes require a caller-authored `perms.jobdb` or
`perms.limit.jobdb` policy. Limits intersect and cannot be enlarged downstream.

A secret supplied through include `inputs` may be forwarded as `${{ inputs.token }}`
to a secret-enabled destination. It does not authorize independent references to
that secret's name or add it to any lease. Include permissions do not authorize
defaults from another op repo automatically: bind the input explicitly or grant
the secret on the invoking node.

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

Use context paths visible to the op instead of hard-coded paths. Operations share the worker environment; c2j does not translate paths into nested containers.

## Object Checkpoints

Route an object's complete value with `${{ ... }}`:

```yaml
inputs:
  session: "${{ sequence.previous.outputs.session }}"
```

An object bundles op-owned metadata and files behind a typed immutable reference. Every consumer restores a fresh writable copy. Multiple nodes may reference the same earlier checkpoint without impacting each other; no special fork declaration or `object_bindings` is needed. To continue a branch, explicitly select that branch's output. There is no automatic latest-session slot.

Objects can travel through outputs, nested maps/lists, states, includes, child jobs and later jobs in the same tenant. A recipe accepting a checkpoint declares `type: object` and optionally `object_type: c2ops.codex.session/v1` in its `input_schema`, then binds the submitted input into root `inputs` as usual. Use `${{ ... }}` for the complete value; `{{ ... }}` string rendering is unsuitable for object maps.

Checkpoint backing files are excluded from recipe artifact maps. Do not manually route the object archive, session ID or rollout as separate artifacts. The originating JobDB task artifacts must remain retained while references are needed. Existing Git snapshot forwarding is unchanged.

An op must implement the object protocol before using this input. In particular, existing c2ops Codex versions do not gain a `session` input just because the c2j runtime supports objects.
