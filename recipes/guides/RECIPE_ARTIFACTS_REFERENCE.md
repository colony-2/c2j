# Recipe Artifacts Reference

This guide explains how recipe writers can produce, reference, and move artifacts between steps, including the inbox/outbox directories and the special thin pack artifact.

## 1. What an Artifact Is

An artifact is a named blob (often a file) that flows from one step to another. Artifacts can be:
- Emitted by an op.
- Captured from the outbox directory.
- Referenced by later nodes using templates.
- Materialized into a step-local inbox directory before an op runs.

## 2. Producing Artifacts

### 2.1 From an Op
Some ops emit artifacts as part of their outputs. These show up in templates under:
`sequence.<node-id>.artifacts["name"]` (or `states.<state-id>.artifacts["name"]`).

### 2.2 From the Outbox
Every op execution gets an `outbox` directory:
- Template path: `context.environment.outbox`
- Any files created under this directory become artifacts automatically.
- Artifact names are the file paths relative to the outbox (subdirectories included).

Example:
```yaml
- id: write_outbox
  op: command_execution
  inputs:
    working_directory: "{{ context.environment.outbox }}"
    run: |
      mkdir -p subdir
      head -c 16 /dev/urandom > subdir/random.bin
```

This produces an artifact named `subdir/random.bin`.

## 3. Consuming Artifacts

### 3.1 Pass Artifact Keys in Inputs
You can reference artifacts directly in inputs using templates.

Example:
```yaml
- id: consume
  op: test_consume_artifact
  inputs:
    artifact: "${{ sequence.emit.artifacts[\"foo\"] }}"
```

Use a single CEL expression (`${{ ... }}`) when passing artifact keys. This preserves the artifact-key type instead of coercing to text.

This also applies to list-typed artifact-key inputs for ops that support them.

`codex.exec` note:
- `codex.exec` does not consume artifact-key skill sources.
- Use `skills` refs (for example `github.com/acme/codex-platform-skills/.agents/skills@main`) to provide skill bundles.

### 3.2 Materialize Artifacts into the Inbox
Some ops (like `command_execution`) support an `artifacts:` block to materialize referenced artifacts on disk before the op runs.

Rules:
- `artifacts` is a sibling of `inputs`.
- Values must resolve to artifact references.
- Only ops that support inbox bindings may use `artifacts`.
- Bindings are written to `context.environment.inbox` before the op executes.

Example:
```yaml
- id: read_inbox
  op: command_execution
  inputs:
    working_directory: "{{ context.environment.inbox }}"
    run: "cat payload.txt"
  artifacts:
    payload.txt: "${{ sequence.write_outbox.artifacts[\"payload.txt\"] }}"
```

Binding name rules:
- Must be a relative path, no `..` segments.
- If the name ends with `/`, the artifact file name is appended.
- Collisions or path escapes are errors.

## 4. Crossing Scope Boundaries (Important)

Artifacts are stored on the node that produced them. If an outer scope needs an inner artifact, export the artifact key through the container `outputs:` map.

### 4.1 Inner Sequence -> Outer Scope

```yaml
- id: outer
  sequence:
    - id: build
      sequence:
        - id: write
          op: command_execution
          inputs:
            working_directory: "{{ context.environment.outbox }}"
            run: "printf 'hello' > payload.txt"
      outputs:
        payload_artifact: '${{ sequence.write.artifacts["payload.txt"] }}'
    - id: consume
      op: command_execution
      inputs:
        working_directory: "{{ context.environment.inbox }}"
        run: "cat payload.txt"
      artifacts:
        payload.txt: '${{ sequence.build.outputs.payload_artifact }}'
```

### 4.2 Inner State Machine -> Outer Scope

```yaml
- id: flow
  state:
    initial: generate
    states:
      generate:
        op: command_execution
        inputs:
          working_directory: "{{ context.environment.outbox }}"
          run: "printf 'report' > report.txt"
        transitions:
          - to: done
            when: true
      done:
        op: echo_activity
        inputs:
          message: done
  outputs:
    report_artifact: '${{ states.generate.artifacts["report.txt"] }}'

- id: read_report
  op: command_execution
  inputs:
    working_directory: "{{ context.environment.inbox }}"
    run: "cat report.txt"
  artifacts:
    report.txt: '${{ sequence.flow.outputs.report_artifact }}'
```

### 4.3 Child Recipe -> Parent Recipe

Use `recipe.run_and_get_result`, `recipe.await_result`, or `recipe.get_result`:
- Child value outputs are under `sequence.<step-id>.outputs.outputs.*`.
- Child artifacts are attached to the parent step artifacts (`sequence.<step-id>.artifacts`).

```yaml
- id: run_child
  op: recipe.run_and_get_result
  inputs:
    name: child-artifact
    inputs: {}
    artifacts: []
    git_ref: "{{ inputs.git_ref }}"

- id: use_child_artifact
  op: command_execution
  inputs:
    working_directory: "{{ context.environment.inbox }}"
    run: "cat foo"
  artifacts:
    foo: '${{ sequence.run_child.artifacts["foo"] }}'

outputs:
  child_name: "{{ sequence.run_child.outputs.outputs.name }}"
```

## 5. Propagation and Template Access

Artifacts are stored per node and are accessible in templates:
- Sequence nodes: `sequence.<node-id>.artifacts`
- State nodes: `states.<state-id>.artifacts`
- Prior runs (retries/loops): `sequence.<node-id>.runs[].artifacts`

Composite-node caveat:
- For a `sequence`, the sequence-level artifacts are taken from the final executed child node.
- For a `state` machine, the state-machine-level artifacts are taken from the final state.
- If you need a specific inner artifact regardless of terminal path, export its key via the container `outputs:` map.

Artifacts are preserved on failure if the op ran and produced them, which is helpful for debugging output (logs, partial files).

## 6. Inbox and Outbox Paths

The executor replaces sentinel values in templates at runtime:
- `context.environment.inbox` -> per-op inbox directory
- `context.environment.outbox` -> per-op outbox directory

These are always local to the current task. The only way to move data between steps is by emitting artifacts and referencing them.

## 7. Thin Pack Artifact (Git State)

The git workspace controller uses a special artifact named:

```
__git_state_thin_pack__
```

Purpose:
- Encodes git state so the next step can restore the workspace.
- Automatically forwarded between tasks.

Behavior:
- It is not visible as a normal input artifact for ops.
- If a step makes changes, a new thin pack is produced.
- If no changes were made, the previous thin pack is passed through.
- Diff artifacts (for example, `diff_from_parent.diff`) may also be produced.

Recipe authors generally should not bind or inspect the thin pack directly; treat it as internal state propagation for git-backed recipes.

## 8. Quick Reference Examples

### Emit -> Consume by Key
```yaml
- id: emit
  op: test_emit_artifact
- id: consume
  op: test_consume_artifact
  inputs:
    artifact: "${{ sequence.emit.artifacts[\"foo\"] }}"
```

### Outbox -> Inbox
```yaml
- id: write_outbox
  op: command_execution
  inputs:
    working_directory: "{{ context.environment.outbox }}"
    run: "printf 'hello inbox' > payload.txt"
- id: read_inbox
  op: command_execution
  inputs:
    working_directory: "{{ context.environment.inbox }}"
    run: "cat payload.txt"
  artifacts:
    payload.txt: "${{ sequence.write_outbox.artifacts[\"payload.txt\"] }}"
```
