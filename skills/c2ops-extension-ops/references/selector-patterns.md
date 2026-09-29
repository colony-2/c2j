<!-- Sources: C2J_SKILLS_IMPLEMENTATION_PLAN.md; pkg/ops/extensions/execution_op.go; pkg/ops/extensions/extension_ops.go; c2j recipe template/artifact references. -->

# Selector Patterns

## Base Invocation

Use `extension_execution` and put op-specific parameters under nested `inputs`.

```yaml
sequence:
  - id: ask
    op: extension_execution
    inputs:
      selector: git+https://github.com/colony-2/c2ops.git//llm@main
      inputs:
        prompt: "{{ inputs.prompt }}"
outputs:
  response: "{{ sequence.ask.outputs.response }}"
```

If the extension manifest declares defaults, c2j applies them before execution. The extension validates nested inputs against its `input_schema` and validates stdout against `output_schema`.

## Codex

```yaml
- id: codex
  op: extension_execution
  inputs:
    selector: git+https://github.com/colony-2/c2ops.git//codex@main
    inputs:
      prompt: "{{ inputs.prompt }}"
```

For skill execution through c2ops:

```yaml
- id: run_skill
  op: extension_execution
  inputs:
    selector: git+https://github.com/colony-2/c2ops.git//codex/run_skill@main
    inputs:
      skill: c2j-recipes
      prompt: "{{ inputs.prompt }}"
```

## Rule Gate

```yaml
state:
  initial: gate
  states:
    gate:
      op: extension_execution
      inputs:
        selector: git+https://github.com/colony-2/c2ops.git//rule_gate@main
        inputs:
          subject: "{{ inputs.subject }}"
      transitions:
        - to: pass
          when: "states.gate.outputs.ok == true"
        - to: fail
          when: "states.gate.outputs.ok != true"
```

## GitHub Actions

Use `gha` for one workflow-style action and `gha-many` for fan-out. Pass structured inputs as raw template values, not JSON strings, unless the op schema specifically expects a string.

```yaml
- id: ci
  op: extension_execution
  inputs:
    selector: git+https://github.com/colony-2/c2ops.git//gha@main
    inputs:
      ref: "{{ context.git.ref }}"
      payload: "{{ inputs.payload }}"
```

## Pydantic

Use `pydantic` when the recipe needs schema validation or structured parsing.

```yaml
- id: validate
  op: extension_execution
  inputs:
    selector: git+https://github.com/colony-2/c2ops.git//pydantic@main
    inputs:
      data: "{{ sequence.collect.outputs }}"
```

## Artifacts

Bind artifacts at the recipe node level, then pass op-visible inbox paths in nested inputs.

```yaml
- id: analyze
  op: extension_execution
  artifacts:
    report.md: '${{ context.artifacts["report.md"] }}'
  inputs:
    selector: git+https://github.com/colony-2/c2ops.git//codex@main
    inputs:
      prompt: 'Review "${{ context.environment.op.inbox }}/report.md".'
```

## Sandbox

If the extension supports a `sandbox` input, include it under nested `inputs`. c2j strips the sandbox field from the extension JSON payload and uses it to configure execution.

```yaml
inputs:
  selector: git+https://github.com/colony-2/c2ops.git//aider@main
  inputs:
    prompt: "{{ inputs.prompt }}"
    sandbox:
      type: shai
```

## Immutable Session Objects

For an op version that implements objects, route `session: "${{ sequence.previous.outputs.session }}"` as an ordinary input. Confirm the selected manifest actually declares `x-c2j-object-type`; older Codex ops still expose `sessionId` and legacy artifact state.

Extension manifests annotate checkpoint fields with `type: object` and `x-c2j-object-type: c2ops.codex.session/v1`. Before invoking the process, c2j replaces each reference with `{ref, metadata, files}`. File paths are private writable copies in the op's filesystem view. An explicit object must be the complete source of resume state; do not overlay a shared session-ID cache or an implicit latest checkpoint.

To publish, write export parts beneath `C2J_OBJECT_OUTBOX` and emit `{"output":{"session":{"$object":"next"}},"objects":{"next":{"type":"c2ops.codex.session/v1","metadata":{"session_id":"..."},"files":{"home":"<absolute staging path>"}}}}`. c2j seals the files and replaces the marker with the durable reference. Returning the input descriptor's `ref` preserves the original checkpoint. Keep ordinary user deliverables in the regular artifact outbox.

Both later consumers may select the same earlier object; each starts with its exact state. Session IDs may match while checkpoint contents differ. A Codex export must preserve all supported resumable assets, including required database/rollout state, and omit credentials and transient files. Resolve relocated paths inside the adapter. Test branching, retries and worker restart without shared caches before changing recipe routing.
