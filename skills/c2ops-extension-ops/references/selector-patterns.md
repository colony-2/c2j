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
