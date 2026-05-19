# Requirements: c2j Op-Visible Path Context

## Problem

Recipe authors need to tell an op where to read inbox artifacts and where to write outbox artifacts.

Today the available template fields, for example `context.environment.inbox` and `context.environment.outbox`, describe the engine-created directories from the host/runtime view. That works when an extension op runs directly on the host, but it is not guaranteed to be the path visible from inside a sandbox.

Historically, sandboxed Codex flows used paths like `/src/inbox` and `/src/outbox`. After c2ops `codex@main` moved to direct execution, those paths were wrong; the correct paths were the c2j host paths under `/tmp/recipe-worktree-.../inbox` and `/tmp/recipe-worktree-.../outbox`. If a user turns sandboxing back on, the opposite can be true.

Recipes need one path property that is correct from the op process viewpoint whether the op runs with `sandbox.type: none`, `sandbox.type: shai`, or a future sandbox provider.

## Goals

1. Give recipe authors a stable template property for paths visible inside the op's actual execution environment.
2. Keep existing `context.environment.*` behavior backward compatible.
3. Make extension-op schema defaults able to use the stable op-visible paths.
4. Support both direct execution and sandboxed execution without recipe prompt changes.
5. Make failures explicit when c2j cannot map a required path into the selected sandbox.

## Non-Goals

- Do not require every op to run sandboxed.
- Do not change existing artifact capture semantics.
- Do not make recipes responsible for knowing sandbox mount targets.
- Do not overload `context.environment.inbox` or `context.environment.outbox` in a way that breaks current recipes.

## Requirements

### R1: Op-visible path context

c2j must expose an op-visible path namespace during template rendering.

Proposed shape:

```text
context.environment.op.workdir
context.environment.op.worktree_path
context.environment.op.inbox
context.environment.op.outbox
```

Each value must be the path that the op process can use at runtime.

### R2: Host path context remains available

The existing flat fields must remain backward compatible:

```text
context.environment.workdir
context.environment.worktree_path
context.environment.inbox
context.environment.outbox
```

These continue to mean the engine/runtime host paths. c2j may also add explicit host aliases:

```text
context.environment.host.workdir
context.environment.host.worktree_path
context.environment.host.inbox
context.environment.host.outbox
```

### R3: Direct execution maps op-visible paths to host paths

For `sandbox.type: none`, `context.environment.op.*` must equal the corresponding host path values.

Example:

```text
context.environment.op.inbox == context.environment.inbox
context.environment.op.outbox == context.environment.outbox
```

### R4: Sandboxed execution maps op-visible paths to sandbox paths

For `sandbox.type: shai`, `context.environment.op.*` must use the sandbox-visible mount targets.

Example:

```text
context.environment.inbox      = /tmp/recipe-worktree-123/inbox
context.environment.op.inbox   = /src/inbox
context.environment.outbox     = /tmp/recipe-worktree-123/outbox
context.environment.op.outbox  = /src/outbox
```

The exact sandbox paths are owned by c2j and the sandbox adapter. Recipe authors should not hard-code them.

### R5: Template defaults see op-visible paths

Selector-backed extension op schema defaults must be able to reference `context.environment.op.*`.

Example c2ops `codex` defaults after c2j support lands:

```yaml
input_schema:
  properties:
    workdir_path:
      type: string
      default: "{{ context.environment.op.workdir }}"
    worktree_path:
      type: string
      default: "{{ context.environment.op.worktree_path }}"
    artifact_inbox_path:
      type: string
      default: "{{ context.environment.op.inbox }}"
    artifact_outbox_path:
      type: string
      default: "{{ context.environment.op.outbox }}"
```

### R6: Recipe prompts can use the same property

Recipes should be able to pass paths to agentic ops without knowing sandbox mode:

```yaml
prompt: |
  Input artifacts:
  - `{{ context.environment.op.inbox }}/requirements/plan.json`

  Required output artifacts:
  - `{{ context.environment.op.outbox }}/implementation/latest-status.json`
```

### R7: Sandbox resolution precedes path rendering

c2j must know the effective sandbox mode before resolving any templates that use `context.environment.op.*`.

For extension ops, this means c2j must process the reserved `inputs.sandbox` field early enough to build the op-visible path context before resolving:

- extension input schema defaults
- authored extension inputs
- prompt strings
- artifact path defaults

To avoid a circular dependency, `inputs.sandbox.type` should either be literal or be resolved in an initial pass that only has access to the host-view context. Templates inside the sandbox config must not depend on `context.environment.op.*`.

### R8: Missing mappings fail before op execution

If the selected sandbox cannot make `workdir`, `worktree_path`, `inbox`, or `outbox` visible inside the op process, c2j must fail validation or pre-execution setup with a clear error.

Example error shape:

```text
op-visible path mapping failed: sandbox "shai" did not provide an outbox mount target
```

### R9: Tests must cover both views

c2j must add tests for:

- `sandbox.type: none`: op-visible paths equal host paths.
- `sandbox.type: shai`: op-visible paths equal sandbox mount targets.
- extension op schema defaults can reference `context.environment.op.*`.
- recipe prompt strings can reference `context.environment.op.*`.
- missing sandbox mount mappings fail before the extension command starts.

## Proposed Implementation

### 1. Introduce a path view resolver

Add a small resolver in c2j that takes:

- host operation paths already created by c2j
- effective sandbox config
- sandbox adapter path mappings

and returns:

```go
type OperationPathViews struct {
    Host OperationPaths
    Op   OperationPaths
}

type OperationPaths struct {
    Workdir      string
    WorktreePath string
    Inbox        string
    Outbox       string
}
```

For direct execution:

```go
views.Op = views.Host
```

For sandbox execution:

```go
views.Op = sandboxAdapter.VisiblePaths(views.Host)
```

### 2. Extend template context

Add the resolver output to the environment context:

```json
{
  "environment": {
    "workdir": "/tmp/recipe-worktree-123",
    "worktree_path": "/tmp/recipe-worktree-123/worktree",
    "inbox": "/tmp/recipe-worktree-123/inbox",
    "outbox": "/tmp/recipe-worktree-123/outbox",
    "host": {
      "workdir": "/tmp/recipe-worktree-123",
      "worktree_path": "/tmp/recipe-worktree-123/worktree",
      "inbox": "/tmp/recipe-worktree-123/inbox",
      "outbox": "/tmp/recipe-worktree-123/outbox"
    },
    "op": {
      "workdir": "/src",
      "worktree_path": "/src/git",
      "inbox": "/src/inbox",
      "outbox": "/src/outbox"
    }
  }
}
```

For direct execution, the `op` values would be the `/tmp/recipe-worktree-...` paths.

### 3. Resolve extension sandbox before extension input templates

For extension ops, c2j already treats `inputs.sandbox` as reserved and does not pass it through to the extension payload. That field should also determine the op-visible path view before input defaults and prompt templates are rendered.

Recommended order:

1. Resolve selector to manifest.
2. Read authored raw `inputs.sandbox`, applying a default sandbox policy if omitted.
3. Create host operation directories.
4. Compute `context.environment.host.*` and `context.environment.op.*`.
5. Apply extension schema defaults.
6. Resolve all extension input templates.
7. Validate the final payload against the extension input schema.
8. Execute the extension with the selected sandbox.

### 4. Update c2ops defaults after c2j support

Once c2j exposes `context.environment.op.*`, c2ops `codex` should change its default input paths from host-view fields to op-view fields:

```yaml
workdir_path:
  default: "{{ context.environment.op.workdir }}"
worktree_path:
  default: "{{ context.environment.op.worktree_path }}"
artifact_inbox_path:
  default: "{{ context.environment.op.inbox }}"
artifact_outbox_path:
  default: "{{ context.environment.op.outbox }}"
```

Recipes can then omit those fields unless they intentionally need to override them.

### 5. Migration guidance

Until c2j implements this context, recipes must use whichever path view matches the selected execution mode:

- direct c2ops Codex: `{{ context.environment.inbox }}` and `{{ context.environment.outbox }}`
- sandboxed Codex: sandbox-visible paths such as `/src/inbox` and `/src/outbox`

After c2j implements this proposal, recipes should use:

```text
{{ context.environment.op.inbox }}
{{ context.environment.op.outbox }}
{{ context.environment.op.worktree_path }}
```

and should avoid direct references to `/src/inbox`, `/src/outbox`, or host `/tmp/recipe-worktree-*` paths in prompts.

## Acceptance Criteria

1. A recipe using `{{ context.environment.op.inbox }}` and `{{ context.environment.op.outbox }}` works with `sandbox.type: none`.
2. The same recipe works with `sandbox.type: shai` without changing the prompt or op inputs.
3. A selector-backed extension op can define input defaults using `context.environment.op.*`.
4. Existing recipes using `context.environment.inbox` and `context.environment.outbox` continue to render as before.
5. c2j test fixtures cover direct, sandboxed, and missing-mount cases.
