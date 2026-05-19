# Recipe Failure Handling User Guide

Recipe `catch:` blocks let a recipe handle selected runtime failures explicitly
instead of failing the job immediately.

Catch handling is for runtime node failures: task errors, timeouts,
cancellations, system errors, and unknown runtime errors. Recipe authoring
errors, malformed templates, invalid catch definitions, and schema errors still
fail validation or execution directly.

## Basic Syntax

Add `catch:` to any recipe node metadata. Clauses are evaluated in order and the
first matching clause wins.

```yaml
catch:
  - id: known_failure
    when: failure.kind == "task_error" && failure.code == "E_BAD_INPUT"
    to: review
    payload:
      reason: ${{ failure.message }}

  - id: fallback
    when: true
    continue:
      outputs:
        status: recovered
```

Each clause must have exactly one action:

- `to`: route to a state in the current state machine.
- `continue.outputs`: complete the failed node with substitute outputs.
- `fail`: rewrite the failure and propagate it to a parent catch or the job.

`payload` is only valid with `to`.

## Failure Object

Catch `when`, `payload`, `continue.outputs`, and `fail` fields can read
`failure`.

```yaml
failure:
  kind: timeout | task_error | system_error | cancellation | unknown
  code: string
  message: string
  retryable: boolean
  node:
    id: string
    path: string
    type: op | sequence | state_machine | state
    op: string
  cause:
    kind: string
    code: string
    message: string
```

Helpers are available in CEL and Go templates:

- `failure_is(failure, "task_error")`
- `failure_message_contains(failure, "text")`
- `failure_message_matches(failure, "regex")`
- `failure_has_code(failure, "E_BAD_INPUT")`
- `failure_originates_from(failure, "node_id_or_path")`
- `failure_root_cause(failure)`

## State Machine Routing

State catch clauses can route to another state. The target state receives a
failure-aware transition:

```yaml
state:
  initial: implement
  states:
    implement:
      op: command_execution
      inputs:
        run: make test
      catch:
        - id: tests_failed
          when: failure_is(failure, "task_error")
          to: review
          payload:
            summary: ${{ failure.message }}

    review:
      op: command_execution
      inputs:
        run: |
          echo '${{ transition.failure.kind }}'
          echo '${{ transition.payload.summary }}'
```

State-machine container catch clauses act as fallbacks for failures not handled
by the state itself.

```yaml
catch:
  - id: machine_fallback
    when: failure.kind == "task_error"
    to: review
state:
  initial: implement
  states:
    implement:
      op: command_execution
      inputs: {run: make test}
    review:
      op: command_execution
      inputs: {run: echo review}
```

## Continue Outputs

`continue.outputs` substitutes outputs for the failed node.

For a failed state, normal transitions can inspect the substitute outputs as
`outputs.*`:

```yaml
state:
  initial: fetch
  states:
    fetch:
      op: command_execution
      inputs: {run: ./fetch_optional_data}
      catch:
        - when: failure_has_code(failure, "NOT_FOUND")
          continue:
            outputs:
              status: missing
      transitions:
        - to: done
          when: outputs.status == "missing"
```

For a failed sequence child, substitute outputs are written under
`sequence.<id>.outputs` and later siblings continue:

```yaml
sequence:
  - id: optional_data
    op: command_execution
    inputs: {run: ./fetch_optional_data}
    catch:
      - when: failure_has_code(failure, "NOT_FOUND")
        continue:
          outputs:
            value: default

  - id: use_data
    op: command_execution
    inputs:
      run: echo '${{ sequence.optional_data.outputs.value }}'
```

For a root op, root sequence, or root state machine, `continue.outputs` becomes
the recipe result.

## Rewriting Failures

Use `fail` to replace the visible failure while preserving the original as
`failure.cause` for parent catch blocks.

```yaml
catch:
  - id: classify_child_failure
    when: failure.kind == "task_error"
    fail:
      kind: task_error
      code: REVIEW_REQUIRED
      message: "Review required: ${{ failure.message }}"
```

A parent catch can match the rewritten failure:

```yaml
catch:
  - when: failure.code == "REVIEW_REQUIRED" && failure.cause.kind == "task_error"
    to: review
```

## Retry Ordering

When a node has `catch`, each recipe-level attempt is exposed to catch before the
next retry is scheduled.

- If a catch clause handles the failure with `to` or `continue`, retry is
  suppressed.
- If no clause matches, the node retries according to its `retry` policy.
- If a clause uses `fail`, the rewritten failure remains retry-eligible.
- Nodes without catch keep the existing task retry behavior.

`retry.maximum_attempts` is total attempts, including the first attempt.

## Validation

Validation rejects catch blocks before a recipe run when:

- `catch` is not a list of objects.
- A clause has zero actions or multiple actions.
- `payload` is used without `to`.
- `to` is used outside a state machine.
- `to` references an unknown state.
- `when` is invalid CEL or does not return a boolean.
- `failure` fields are misspelled, such as `failure.knd`.
- `fail.kind` is not one of the supported failure kinds.

States reached by catch routes are validated with `transition.failure`
available. States reached by both normal and catch transitions are validated
with both transition shapes.

## Runnable Examples

The worker fixture suite includes executable examples for the main handling
paths:

- `pkg/worker/test-fixtures/recipes/failure-handling-state-route.yaml` routes a
  failed state to a review state and reads `transition.failure`.
- `pkg/worker/test-fixtures/recipes/failure-handling-sequence-continue.yaml`
  recovers a failed sequence child with `continue.outputs` and lets the next
  sibling consume the substitute output.
- `pkg/worker/test-fixtures/recipes/failure-handling-fail-rewrite.yaml`
  rewrites a state failure with `fail`, then lets the state-machine catch route
  on the rewritten code.

Run them with:

```sh
go test ./pkg/worker/test-fixtures -run 'TestAllRecipes/failure-handling' -count=1
```
