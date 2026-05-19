# Using Op-Visible Paths

This guide explains how recipe authors and extension authors should pass file
paths to ops that may run either directly on the host or inside a sandbox.

## Short Version

Use `context.environment.op.*` for any path that the op process itself must
read from or write to.

```yaml
run: |
  ls "{{ context.environment.op.inbox }}"
  mkdir -p "{{ context.environment.op.outbox }}/results"
  printf '{"ok":true}\n' > "{{ context.environment.op.outbox }}/results/status.json"
```

That same recipe works when the op runs directly on the host and when it runs
inside a supported sandbox.

Use `context.environment.host.*` only when you intentionally need the path as
seen by the c2j worker host.

## Available Path Context

| Field | Meaning |
| --- | --- |
| `context.environment.op.workdir` | Operation workspace root as seen by the running op process. |
| `context.environment.op.worktree_path` | Git worktree path as seen by the running op process. |
| `context.environment.op.inbox` | Artifact inbox path as seen by the running op process. |
| `context.environment.op.outbox` | Artifact outbox path as seen by the running op process. |
| `context.environment.host.workdir` | Operation workspace root on the c2j worker host. |
| `context.environment.host.worktree_path` | Git worktree path on the c2j worker host. |
| `context.environment.host.inbox` | Artifact inbox path on the c2j worker host. |
| `context.environment.host.outbox` | Artifact outbox path on the c2j worker host. |

The older flat fields still exist and remain host-view paths:

```text
context.environment.workdir
context.environment.worktree_path
context.environment.inbox
context.environment.outbox
```

For new recipes, prefer the explicit namespace:

- `context.environment.op.*` when a command, agent, or extension process will
  use the path.
- `context.environment.host.*` when a host-side integration needs the path.

## Default Values By Sandbox Mode

When `sandbox` is omitted or `sandbox.type: none`, op-visible paths equal the
host paths:

| Field | Value |
| --- | --- |
| `context.environment.op.workdir` | same as `context.environment.host.workdir` |
| `context.environment.op.worktree_path` | same as `context.environment.host.worktree_path` |
| `context.environment.op.inbox` | same as `context.environment.host.inbox` |
| `context.environment.op.outbox` | same as `context.environment.host.outbox` |

When `sandbox.type: shai`, c2j uses the existing Codex/Shai sandbox path
pattern:

| Field | Default sandbox-visible value |
| --- | --- |
| `context.environment.op.workdir` | `/src` |
| `context.environment.op.worktree_path` | `/src/git` |
| `context.environment.op.inbox` | `/src/inbox` |
| `context.environment.op.outbox` | `/src/outbox` |

The host fields do not change when sandboxing is enabled. For example,
`context.environment.host.inbox` remains the c2j worker host path, while
`context.environment.op.inbox` becomes the path visible inside the sandbox.

## Command Execution

`command_execution` now defaults `working_directory` to:

```yaml
working_directory: "{{ context.environment.op.worktree_path }}"
```

That means most commands do not need to set `working_directory` explicitly.

Direct execution:

```yaml
sequence:
  - id: write_result
    op: command_execution
    inputs:
      run: |
        mkdir -p "{{ context.environment.op.outbox }}/results"
        printf '{"status":"ok"}\n' > "{{ context.environment.op.outbox }}/results/status.json"
```

Sandboxed execution:

```yaml
sequence:
  - id: inspect_repo
    op: command_execution
    inputs:
      sandbox:
        type: shai
      run: |
        pwd
        ls "{{ context.environment.op.worktree_path }}"
        ls "{{ context.environment.op.inbox }}"
```

The recipe still uses `context.environment.op.*`. c2j chooses the correct value
for the selected execution mode.

## Extension Ops

Extension op schema defaults can use op-visible paths. This is the recommended
way for extension authors to expose path inputs that should work in both direct
and sandboxed execution.

Example `op.yaml`:

```yaml
name: summarize_artifacts
run: python3 main.py

input_schema:
  type: object
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

Recipe authors can then enable sandboxing without overriding those path fields:

```yaml
sequence:
  - id: summarize
    op: ./tools/ops/summarize-artifacts
    inputs:
      sandbox:
        type: shai
```

The reserved `sandbox` input controls execution. It is not part of the payload
delivered to the extension process, so extension authors should not include
`sandbox` in their `input_schema`.

## Passing Paths To Prompts Or Agents

When an op receives a prompt or instruction string that mentions filesystem
locations, use op-visible paths:

```yaml
prompt: |
  Read source files from:
  {{ context.environment.op.worktree_path }}

  Read input artifacts from:
  {{ context.environment.op.inbox }}

  Write the final report to:
  {{ context.environment.op.outbox }}/final-report.md
```

Avoid hard-coding `/src`, `/src/git`, `/src/inbox`, or `/src/outbox` in prompts.
Those are the current Shai defaults, but `context.environment.op.*` keeps the
recipe independent of the execution mode.

## Custom Sandbox Paths

Most recipes should only set the sandbox type:

```yaml
sandbox:
  type: shai
```

Use `sandbox.paths` only when you need a custom sandbox layout. Each path can
define:

- `host`: absolute source path on the worker host.
- `sandbox`: absolute target path inside the sandbox.
- `mode`: `rw` or `ro`; omitted means `rw`.

Example:

```yaml
sandbox:
  type: shai
  paths:
    worktree_path:
      sandbox: /workspace/repo
    inbox:
      sandbox: /workspace/inbox
      mode: ro
    outbox:
      sandbox: /workspace/outbox
```

If `host` is omitted, c2j uses the normal host path for that operation
directory. If `sandbox` is omitted for `sandbox.type: shai`, c2j uses the Shai
defaults listed above.

You can use host-view path context in `sandbox.paths.host` when needed:

```yaml
sandbox:
  type: shai
  paths:
    inbox:
      host: "{{ context.environment.host.inbox }}"
      sandbox: /inputs
      mode: ro
```

Do not use `context.environment.op.*` inside `sandbox`. The sandbox config is
what defines the op-visible paths, so c2j rejects that circular reference.

## Validation Rules And Limits

- Supported sandbox types are `none` and `shai`.
- `sandbox.type: shai` is not supported on Windows hosts.
- Sandbox path config is strict. Unknown fields fail validation.
- Host paths must be absolute paths.
- Shai sandbox paths must be absolute sandbox paths.
- Mount modes must be `ro` or `rw`.
- Duplicate sandbox mount targets are allowed only when they map to the same
  host source with the same mode.
- There is no inline Shai config escape hatch in `inputs.sandbox`.

## Migration Guide

Switch paths passed to commands, prompts, agents, and extension inputs:

```diff
- "{{ context.environment.inbox }}"
+ "{{ context.environment.op.inbox }}"

- "{{ context.environment.outbox }}"
+ "{{ context.environment.op.outbox }}"
```

Switch hard-coded Shai paths when the recipe should also work without a
sandbox:

```diff
- /src/git
+ {{ context.environment.op.worktree_path }}

- /src/inbox
+ {{ context.environment.op.inbox }}

- /src/outbox
+ {{ context.environment.op.outbox }}
```

Keep `context.environment.host.*` or the older flat fields only when the host
path is the value you actually want.
