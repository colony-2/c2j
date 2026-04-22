# Sequence Recipes

Use a `sequence` to run a set of steps in order. Sequences are ideal when you want deterministic, linear flow with optional conditional skips, and you need to pass data from earlier steps to later ones.

## Building Blocks
- **Steps list**: `sequence` holds an ordered list of nodes. Each node can be an `op`, nested `sequence`, or `state`.
- **Inputs**: Provided via the sequence’s `inputs:` block; available to every child node as `inputs.*`.
- **Outputs**: Declare under the sequence’s top-level `outputs:` block to expose data to the parent scope.
- **Per-step conditions**: Any node may include `when:` (CEL, no `${{ }}`) to skip that step when false.

```yaml
- id: pipeline
  sequence:
  - id: fetch
    op: http_get
    inputs:
      url: '{{ inputs.api_url }}'
  - id: transform
    op: data_transform
    inputs:
      body: '{{ sequence.fetch.outputs.body }}'
    when: 'sequence.fetch.outputs.status == 200'
  outputs:
    result: '{{ sequence.transform.outputs.data }}'
```

## Execution Model
- Steps run top-to-bottom.
- A step with `when: false` is skipped; later steps still run and can reference any completed sibling outputs.
- The sequence finishes after the last step (no implicit branching).

## Scoping & Data Access
- Inside a sequence:
  - `inputs.*` from the sequence’s own inputs.
  - Sibling outputs via `sequence.<step-id>.outputs.*` (only for steps that have completed).
  - Sibling artifacts via `sequence.<step-id>.artifacts["name"]`.
- Nested nodes obey their own scope: a child `state` uses `states.*`; a child `sequence` uses its own `sequence.*`.
- To share data outside, map it in the outer sequence’s `outputs:` block.
- If you need to export an artifact key, use a single CEL expression in `outputs:`: `${{ sequence.<step-id>.artifacts["name"] }}`.

## Boundary Example: Inner Sequence -> Outer Sequence

Use this pattern when a nested sequence creates artifacts that later outer steps need.

```yaml
- id: pipeline
  sequence:
  - id: prepare
    sequence:
    - id: write
      op: command_execution
      inputs:
        working_directory: "{{ context.environment.outbox }}"
        run: "printf 'hello from nested sequence' > payload.txt"
    outputs:
      payload_artifact: '${{ sequence.write.artifacts["payload.txt"] }}'
  - id: consume
    op: command_execution
    inputs:
      working_directory: "{{ context.environment.inbox }}"
      run: "cat payload.txt"
    artifacts:
      payload.txt: '${{ sequence.prepare.outputs.payload_artifact }}'
  outputs:
    payload_artifact: '${{ sequence.prepare.outputs.payload_artifact }}'
```

## Design Patterns
- **Fetch → Transform → Store**: classic linear pipeline.
- **Inline gating**: use `when` to guard expensive steps (e.g., skip transform if fetch failed).
- **Side-channel artifacts**: produce logs/metrics in early steps; consume them later.
- **Nested control**: drop a `state` inside a sequence step when you need branching without breaking linear top-level flow.

## Authoring Tips
- Name steps by action (`fetch`, `parse`, `summarize`) so `sequence.<id>` reads clearly in templates.
- Order from cheapest to most expensive work; gate heavy steps with `when`.
- Keep outputs small and intentional—export only what the parent needs.
- If a later step requires earlier data, reference it explicitly via `sequence.<step-id>.outputs.*` rather than re-computing.

## Validation Checklist
- Every step has a unique `id`.
- All referenced `sequence.<id>` keys exist and point to completed steps.
- `when` expressions use valid CEL and only reference visible scope fields.
- Required outward data is surfaced via the sequence’s `outputs:` map.
- For artifact export, `outputs:` uses `${{ ... }}` and not mixed text interpolation.

## Minimal Skeleton
```yaml
- id: main
  sequence:
  - id: prep
    op: echo_activity
    inputs:
      message: 'Prep {{ inputs.name }}'
  - id: run
    op: echo_activity
    inputs:
      message: 'Run for {{ sequence.prep.outputs.result }}'
  outputs:
    summary: '{{ sequence.run.outputs.result }}'
```

Start with this structure, then layer in your ops, `when` guards, and nested nodes to fit your workflow.
