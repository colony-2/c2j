# State Machine Recipes

Use the `state` node to model a state machine--branching workflows that react to runtime data. This guide focuses on what recipe authors need to define, transition, and export data from state machines.

## Building Blocks
- **Entry point**: `initial` selects the first state. You can use:
  - String shortcut: `initial: processing`
  - Single transition object: `initial: { to: processing, when: true }`
  - Ordered transition list:
    - `initial:`
    - `  - to: deep_path`
    - `    when: inputs.mode == "deep"`
    - `  - to: processing`
    - `    when: true`
- **States map**: `states` contains named states. Each state is just a regular node (`op`, `sequence`, or nested `state`) plus optional `transitions`.
- **Inputs**: Passed in via the enclosing node’s `inputs:` block, available inside every state as `inputs.*`.
- **Outputs**: Declare under the state machine’s top-level `outputs:` block to expose results to the parent scope.

```yaml
state:
  initial: processing
  states:
    processing:
      op: echo_activity
      inputs:
        message: 'Processing {{ inputs.message }}'
      transitions:
      - to: finalize
        when: outputs.result != null
    finalize:
      op: echo_activity
      inputs:
        message: 'Done'
outputs:
  result: '{{ states.finalize.outputs.result }}'
```

Equivalent explicit form:
```yaml
state:
  initial:
    to: processing
    when: true
  states:
    processing:
      op: echo_activity
      transitions:
      - to: finalize
        when: outputs.result != null
    finalize:
      op: echo_activity
```

## How Transitions Work
- Each state may list `transitions`—ordered rules the runtime evaluates after the state finishes.
- A transition has:
  - `to`: target state name.
  - `when`: CEL condition (no `${{ }}`) evaluated in the current state’s context.
- The first transition whose `when` evaluates to `true` wins.
- **Any state can be terminal**: if no transition matches (or none are defined), the machine completes with the current state’s outputs.

## Scoping & Data Access
- Inside a state, you can reference:
  - `inputs.*` passed to the state machine.
  - Completed states via `states.<state-id>.outputs.*`.
- State artifacts are available via `states.<state-id>.artifacts["name"]`.
- Inside a state that is a `sequence`, you can also use `sequence.<step-id>.outputs.*` for that state’s internal steps.
- To make data visible outside the state machine, map it with the state machine’s top-level `outputs:`. Think “export at each boundary.”
- In `transitions.when`, `outputs.*` refers to the current state outputs (not artifacts); use `states.<id>.artifacts` outside transition conditions.

## Boundary Example: Inner State -> Outer Sequence

Use this when a state emits artifacts that downstream nodes outside the state machine must consume.

```yaml
- id: branching_flow
  state:
    initial: generate
    states:
      generate:
        op: command_execution
        inputs:
          working_directory: "{{ context.environment.outbox }}"
          run: "printf 'state report' > report.txt"
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
    report.txt: '${{ sequence.branching_flow.outputs.report_artifact }}'
```

## Design Patterns
- **Guard/Process/Finalize**: validate inputs → branch to handlers → finalize. Transitions pick the handler based on validation outputs.
- **Retry with branch**: add a counter and route to `retry` or `give_up` states depending on the count.
- **Nested control**: states themselves can be sequence or another state machine to encapsulate rich logic while keeping transitions readable.

## Authoring Tips
- Keep state names verbs or stages (`validate`, `process`, `finalize`) to make transitions self-explanatory.
- Prefer simple, mutually exclusive `when` conditions to avoid ambiguity; ordering matters.
- If multiple transitions could be true, the first one wins—order them from most-specific to fallback.
- Add a “catch-all” transition (`when: true`) when you want explicit fallback behavior; otherwise the state will naturally end the machine.
- Export only the data you need upward to keep templates maintainable.

## Validation Checklist
- `initial` resolves to an existing state (first matching rule wins when using a transition list).
- Every referenced `to` state exists.
- Transition `when` expressions are valid CEL and reference available scope fields.
- Required outputs are exported in the top-level `outputs:` block.
- For artifact export, keep `outputs:` values as raw CEL (`${{ states.<state>.artifacts["name"] }}`).

## Minimal Skeleton
```yaml
state:
  initial: start
  states:
    start:
      op: echo_activity
      inputs:
        message: 'Hello {{ inputs.user }}'
      transitions:
      - to: done
        when: outputs.result != null
    done:
      op: echo_activity
      inputs:
        message: 'Finished'
outputs:
  final_message: '{{ states.done.outputs.result }}'
```

Use this as a starting template, then layer in your own ops, sequences, and branch logic to model the desired workflow.
