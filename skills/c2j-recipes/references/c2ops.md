<!-- Sources: C2J_SKILLS_IMPLEMENTATION_PLAN.md; skills/sources.yaml allowlist. Public c2ops repo docs were not vendored in this checkout. -->

# c2ops Selectors

Use c2ops selectors only from the trusted `colony-2/c2ops` source:

```text
git+https://github.com/colony-2/c2ops.git//<op>@main
```

Known selector paths from the c2j plan:

- `llm`
- `llm2`
- `codex`
- `codex/run_skill`
- `rule_gate`
- `gha`
- `gha-many`
- `pydantic`
- `aider`
- `litellm`

Prefer pinning to an immutable commit for reproducibility when moving beyond local development. `main` is allowed for bootstrap and docs examples.

## Invocation Pattern

```yaml
sequence:
  - id: run_codex
    op: extension_execution
    inputs:
      selector: git+https://github.com/colony-2/c2ops.git//codex@main
      inputs:
        prompt: "{{ inputs.prompt }}"
```

Keep selector inputs in the nested `inputs:` map. Put c2j artifact bindings at the recipe node level if the extension needs files in its inbox.

```yaml
  - id: review
    op: extension_execution
    artifacts:
      brief.md: '${{ context.artifacts["brief.md"] }}'
    inputs:
      selector: git+https://github.com/colony-2/c2ops.git//codex@main
      inputs:
        prompt: 'Read "${{ context.environment.op.inbox }}/brief.md" and summarize risks.'
```

## Rule Gate Pattern

Use `rule_gate` when the recipe needs a structured pass/fail or routing decision. Branch on the op output with a state transition or sequence follow-up step.

```yaml
state:
  initial: gate
  states:
    gate:
      op: extension_execution
      inputs:
        selector: git+https://github.com/colony-2/c2ops.git//rule_gate@main
        inputs:
          subject: "{{ inputs.change_summary }}"
      transitions:
        - to: approved
          when: "states.gate.outputs.ok == true"
        - to: rejected
          when: "states.gate.outputs.ok != true"
```

Read `$c2ops-extension-ops` for deeper selector-specific examples.
