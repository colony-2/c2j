<!-- Sources: pkg/recipe/sequence.go; pkg/recipe/state.go; pkg/recipe/catch.go; pkg/recipe/child_group.go; pkg/worker/compiler/child_group.go; pkg/worker/test-fixtures/recipes/failure-handling-*.yaml; pkg/worker/test-fixtures/recipes/nested-composition.yaml. -->

# Control Flow

## Sequences

Use `sequence` for ordered work. Each node can expose `outputs` and `artifacts` through `sequence.<id>`.

```yaml
sequence:
  - id: render
    op: command_execution
    inputs:
      run: "printf result"
  - id: consume
    op: command_execution
    inputs:
      run: "echo {{ sequence.render.outputs.stdout }}"
outputs:
  result: "{{ sequence.consume.outputs.stdout }}"
```

## State Machines

Use `state` when control flow branches or loops. `initial` accepts a state name, one transition object, or a transition list.

```yaml
state:
  initial: review
  states:
    review:
      op: command_execution
      inputs:
        run: "printf approved"
      transitions:
        - to: done
          when: 'states.review.outputs.stdout == "approved"'
    done:
      op: command_execution
      inputs:
        run: "printf complete"
outputs:
  status: "{{ states.done.outputs.stdout }}"
```

Transition `when` values are CEL expressions without `{{ }}` markers. Transition `payload` is available to the destination state through `transition.payload`.

## Switch Transitions

Transitions may be expressed as a switch table. Nested switch transitions are limited to one nested level.

```yaml
transitions:
  switch: states.classify.outputs.kind
  cases:
    - value: docs
      to: docs_path
    - value: code
      to: code_path
  default:
    to: fallback
```

## Failure Handling

Use `catch` on nodes to route, continue with synthetic outputs, or fail with a structured error. Each catch clause must specify exactly one action: `to`, `continue`, or `fail`.

```yaml
sequence:
  - id: optional
    op: command_execution
    inputs:
      run: "exit 7"
    catch:
      - id: recover_optional
        when: true
        continue:
          outputs:
            stdout: fallback
            success: false
```

State-machine catch routing can pass payload to the next state:

```yaml
catch:
  - id: route_to_review
    when: failure_message_contains(failure, "simulated error")
    to: review
    payload:
      handled_kind: "${{ failure.kind }}"
```

## Child Groups

Use `child_group` for first-class fan-out. Children can be static or generated from `children_from`.

```yaml
child_group:
  mode: run_and_get_result
  children:
    - key: docs
      recipe: docs-review
      required: true
      inputs:
        prompt: "{{ inputs.prompt }}"
    - key: tests
      recipe: test-review
      required: false
  aggregate:
    shape: children
```

Supported child fields include `key`, `recipe`, `cell_name`, `required`, `when`, `skip_reason`, `git_ref`, `inputs`, and `artifacts`.

Use `children_from` with `child` to map over a list. The renderer provides loop locals for each element and index, then enforces unique child keys.

## Child Recipe Ops

For explicit child recipe operations, use:

- `recipe.run_and_get_result` for one child job and returned outputs.
- `recipes.run` for starting many children.
- `recipes.run_and_wait` for starting many children and waiting.
- `recipe.await_result`, `recipe.await_result_soft`, and `recipe.get_result` for later inspection.

Prefer `child_group` when the recipe needs fan-out bookkeeping, optional children, aggregate outputs, or child status summaries.

## Retries And Timeouts

Durations use Go duration strings such as `500ms`, `30s`, `2m`, or `1h`. `command_execution` has `timeout`; registered ops may also have default task timeouts. Use `catch` for explicit recovery behavior after retries/failure.
