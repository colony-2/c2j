# Node Scope Specification

## Purpose

Spell-out the scoping rules for recipe nodes (`op`, `sequence`, `state`) so recipe authors can reference inputs/outputs predictably when composing workflows.

## Node Types

- `op`: single activity or inline op.
- `sequence`: ordered list of child nodes.
- `state`: state machine composed of named states and transitions.

Each node owns its scope; children inherit from the container but cannot see into sibling containers unless outputs are explicitly exported.

## Scope Rules

### Sequence

*Children can access:*
- `inputs` passed to the sequence.
- Sibling outputs via `sequence.<node-id>.outputs.*`.
- Sibling artifacts via `sequence.<node-id>.artifacts["name"]`.
- Outputs defined on the sequence itself (`outputs:` block) once exported upward.

*Example*

```yaml
- id: normalize
  sequence:
    - id: fetch
      op: http_get
      inputs:
        url: '{{ inputs.api_root }}/users'
    - id: format
      op: command_execution
      inputs:
        run: "echo '{{ sequence.fetch.outputs.body }}' | jq '.[] | .name'"
  outputs:
    users_body: '{{ sequence.fetch.outputs.body }}'
```

### State Machine

*Children can access:*
- `inputs` passed to the state machine.
- Previously completed states via `states.<state-id>.outputs.*`.
- Previously completed states via `states.<state-id>.artifacts["name"]`.
- In state `transitions.when`, `outputs.*` refers to the current state's outputs.

`initial` supports either:
- String shortcut: `initial: validate`
- Transition object/list using the same `to` + `when` shape as regular `transitions`

*Example*

```yaml
state:
  initial:
    - to: validate
      when: true
  states:
    validate:
      op: validator
      transitions:
        - to: process
          when: 'states.validate.outputs.valid == true'
    process:
      op: handler
      inputs:
        payload: '{{ states.validate.outputs.cleaned }}'
outputs:
  final_status: '{{ states.process.outputs.status }}'
```

### Op

`op` nodes receive whatever is mapped into `inputs` and can set outputs by returning values from the activity/inline op.

## Mixing Node Types

You can compose nodes by nesting sequences/state machines. Always export data you need outside the container:

```yaml
- id: pipeline
  sequence:
    - id: prepare
      state:
        initial: write_workspace
        states:
          write_workspace:
            op: command_execution
            inputs:
              working_directory: "{{ context.environment.outbox }}"
              run: "printf 'ready' > workspace.txt"
            transitions:
              - to: done
                when: true
          done:
            op: echo_activity
            inputs:
              message: done
        outputs:
          workspace_artifact: '${{ states.write_workspace.artifacts["workspace.txt"] }}'
    - id: summary
      op: command_execution
      inputs:
        working_directory: "{{ context.environment.inbox }}"
        run: "cat workspace.txt"
      artifacts:
        workspace.txt: '${{ sequence.prepare.outputs.workspace_artifact }}'
  outputs:
    workspace_artifact: '${{ sequence.prepare.outputs.workspace_artifact }}'
```

## Encapsulation Rules

1. Each container defines its own scope (`sequence`, `state`).
2. Children can reference siblings only via the container’s namespace (`sequence.<id>`, `states.<id>`).
3. To share data with outer nodes, declare an `outputs:` map on the current container.
4. Nesting requires importing context via `inputs` and exporting via `outputs` at each layer.
5. For artifact keys, use raw CEL expressions (`${{ ... }}`) so the value remains an artifact key.

## Quick Reference

| Node type | Access to inputs | Access to siblings | Export mechanism |
|-----------|------------------|--------------------|------------------|
| `op`      | `inputs.*`       | N/A                | activity outputs + artifacts |
| `sequence`| `inputs.*`       | `sequence.<id>.outputs.*`, `sequence.<id>.artifacts.*` | `outputs:` block |
| `state`   | `inputs.*`       | `states.<id>.outputs.*`, `states.<id>.artifacts.*`   | `outputs:` block |

Use this spec whenever you structure recipes that combine multiple node types, ensuring data flows remain explicit and predictable.
