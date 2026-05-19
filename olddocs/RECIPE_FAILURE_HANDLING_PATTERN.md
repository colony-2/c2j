# Recipe Failure Handling Pattern

## Purpose

This proposal describes a recipe-authoring pattern for catching runtime node
failures and routing the workflow to an explicit recovery path.

It focuses only on how recipe authors express failure handling in YAML. It does
not specify the c2j executor implementation.

## Context From Current Recipes

Current recipes already handle expected business outcomes by returning structured
outputs and branching on them:

- `new-ticket.yaml` routes from implementation to review when the Codex op
  returns `incompleteCategory`.
- `job-validate.yaml` uses `continue_on_error: true` so validation command
  failures become outputs like `exit_code`, `success`, and `timed_out`.
- State machines use ordered `transitions` and switch transitions to route after
  a node completes successfully.

The missing case is a runtime failure where a node does not complete normally:

- an op times out before producing the expected outputs;
- an op returns a task error and `continue_on_error` is not enabled or not
  available;
- the executor or external system fails;
- a sequence fails because one child node failed;
- a state machine fails because one state failed.

Today those failures abort the enclosing workflow. Recipe authors need a way to
turn selected runtime failures into explicit, reviewable workflow paths.

## Goals

- Let authors attach failure handlers to any node: op task, sequence, state
  machine, or state within a state machine.
- Keep normal success transitions unchanged.
- Reuse the current recipe style: ordered rules, CEL conditions, transition
  payloads, explicit outputs, and state-machine routing.
- Make common failure classes easy to match: timeout, task error, system error,
  cancellation, and unknown failure.
- Let recipes branch on structured failure data, including error codes and
  message text when structured codes are unavailable.
- Preserve failure context for human review and later recipe steps.
- Define how node-local retry interacts with catch handling.

## Non-Goals

- Do not replace output-based branching for expected business results.
- Do not make compile-time recipe validation errors recoverable.
- Do not hide behavior inside shell scripts when a recipe-level branch is clearer.
- Do not replace explicit retry states. Recipes can still model retries with
  ordinary states when the retry path needs workflow state or human review.

## Proposed Surface

Add an optional `catch:` block to every recipe node.

The `catch:` block is an ordered list of clauses. Clauses are evaluated only when
the node fails. The first matching clause wins. If no clause matches, the failure
remains unhandled by catch and is then eligible for `retry:` before it propagates
to the parent node.

```yaml
catch:
  - id: timeout_path
    when: 'failure.kind == "timeout"'
    to: timeout_review
    payload:
      failure_kind: "${{ failure.kind }}"
      failure_message: "${{ failure.message }}"

  - id: known_task_error
    when: 'failure.kind == "task_error" && failure_message_contains(failure, "rate limit")'
    to: wait_for_rate_limit
    payload:
      failure: "${{ failure }}"

  - id: system_error
    when: 'failure.kind == "system_error" && failure.retryable == true'
    to: transient_system_failure

  - id: fallback
    when: "true"
    to: failure_review
```

Normal `transitions:` still mean "what happens after this node succeeds."
`catch:` means "what happens if this node fails before normal transitions can be
evaluated."

Nodes may also use the existing scalar retry property, for example `retry: 3`.
Retry is evaluated only after catch handling leaves the failure unhandled. A
`catch` clause that routes with `to:` or completes the node with `continue:`
suppresses retry for that failure.

## Failure Object

Catch clauses receive a `failure` object in addition to the node's normal
scope.

Recommended normalized shape:

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
  timing:
    started_at: string
    failed_at: string
    duration_ms: number
    timeout: string
  task:
    exit_code: number
    status: string
    stderr_tail: string
    stdout_tail: string
  artifacts:
    <artifact-name>: <artifact-ref>
  cause:
    kind: string
    code: string
    message: string
    node:
      path: string
```

Only fields that make sense for a failure need to be present. Authors should use
optional access or helper functions when reading optional fields.

`failure.kind` should be stable enough for portable recipes. `failure.message`
is useful for narrow cases, but recipes should prefer `kind` and `code` when
available.

## Catch Scope

A `catch:` block evaluates in the same node-scoped context that would be
available to that node's `inputs:` and `vars:`, with one additional value:
`failure`.

That means catch `when:` expressions and catch action templates can reference
the usual visible values:

- `inputs.*`
- `vars.*`
- `context.*`
- previous sequence siblings through `sequence.<id>.*`
- previously completed states through `states.<id>.*`
- the invocation's `transition.*` values when the failed node is a state reached
  by a transition
- `failure.*`

This is intentionally not a special failure-only scope. Recipe authors should be
able to use the same data they used to configure the node when deciding how to
handle that node's failure.

The scope should be the invocation snapshot for the failed node. If a state is
re-entered, its catch handlers see the `vars`, `inputs`, and `transition` values
for that specific attempt.

Example:

```yaml
validate:
  op: recipe.run_and_get_result
  inputs:
    name: job-validate
    inputs:
      commands: "${{ vars.validation_commands }}"
  catch:
    - id: validation_timeout_with_commands
      when: >-
        failure.kind == "timeout" &&
        nonempty(vars.validation_commands) &&
        state_exists("outcome_determination")
      to: ready_to_merge_review
      payload:
        validation_failed_by_timeout: true
        attempted_commands: "${{ vars.validation_commands }}"
        failure_message: "${{ failure.message }}"
```

A failed node does not expose successful current-node `outputs` for catch
evaluation. In state `transitions.when`, `outputs.*` means the current state's
successful outputs; in `catch.when`, use `failure.*` plus the normal input scope.
If an op has partial task details, they should be available through `failure`
fields or preserved artifacts.

The same scope applies to catch action templates, including `payload:`,
`continue.outputs`, and `fail`.

## Catch Conditions

Each catch clause uses `when:`. This mirrors state-machine transitions: the
expression is pure CEL, does not use `${{ }}` delimiters, and must evaluate to a
boolean. The first clause whose `when:` is true handles the failure.

```yaml
catch:
  - id: command_timeout
    when: 'failure.kind == "timeout" && failure.node.op == "command_execution"'
    to: use_cached_result

  - id: known_exit_code
    when: >-
      failure.kind == "task_error" &&
      failure.code == "nonzero_exit" &&
      failure.task.exit_code == 2
    to: prompt_for_custom_command

  - id: known_message
    when: >-
      failure.kind == "task_error" &&
      failure_message_contains(failure, "No such file or directory")
    to: repair_missing_path
```

Common fields for `when:` expressions:

| Key | Meaning |
| --- | --- |
| `kind` | Normalized failure kind. |
| `code` | Stable machine-readable error code. |
| `retryable` | Whether the runtime considers the failure safe to retry. |
| `node.id` | Direct node id, when available. |
| `node.path` | Full node path, useful for container-level catches. |
| `node.type` | `op`, `sequence`, `state_machine`, or `state`. |
| `node.op` | Op selector, such as `command_execution` or a c2ops selector. |
| `task.exit_code` | Exit code for task-style failures. |
| `task.status` | Op-specific normalized status, when available. |

Recommended helper functions:

- `failure_is(failure, kind)`
- `failure_message_contains(failure, text)`
- `failure_message_matches(failure, regex)`
- `failure_has_code(failure, code)`
- `failure_originates_from(failure, node_path_or_id)`
- `failure_root_cause(failure)`

These helpers are authoring conveniences for `when:` expressions. Recipes can
still use direct CEL field access for simple cases.

## Catch Actions

A matching catch clause must describe how the failure is handled.

### `to`: Route Inside A State Machine

Use `to:` when the failed node is a state in a state machine, or when the catch
is attached to a state-machine node and should route to one of its states.

```yaml
implement:
  op: git+https://github.com/colony-2/c2ops.git//codex@main
  transitions:
    - to: validate
      when: "true"
  catch:
    - id: implementation_timeout
      when: 'failure.kind == "timeout"'
      to: pre_implementation_followup_review
      payload:
        reason: implementation_timeout
        guidance: >-
          Implementation timed out after ${{ failure.timing.timeout }}.
          Review partial artifacts and decide whether to resume or revise.
```

The target state receives `transition.payload` exactly like a normal transition.
For catch-driven routes, the target state also receives `transition.failure`
with the matched failure object.

### `continue`: Complete The Failed Node

Use `continue:` when a failure should be treated as handled by completing the
failed node with substitute outputs.

What resumes depends on where the catch is attached:

- For a failed sequence step, the sequence continues with the next step and
  `sequence.<id>.outputs` contains the substitute outputs.
- For a failed state, the state is considered complete and normal state
  transitions evaluate with the substitute outputs as the current `outputs`.
- For a failed sequence or state-machine container, the parent node sees the
  container as complete with the substitute outputs.

```yaml
sequence:
  - id: load_cells
    op: command_execution
    inputs:
      timeout: 30s
      run: c2j cells --json
    catch:
      - id: cell_list_unavailable
        when: 'failure.kind == "system_error"'
        continue:
          outputs:
            stdout: "[]"
            stderr: "${{ failure.message }}"
            exit_code: 0
            success: true
```

This is useful only when downstream steps can safely operate on the substitute
outputs. It should be used sparingly because it can hide missing context.

### `fail`: Reclassify Or Annotate Before Propagation

Use `fail:` to make the failure more actionable and then propagate it.

```yaml
catch:
  - id: unhandled_codex_error
    when: 'failure.node.op.contains("//codex@")'
    fail:
      kind: task_error
      code: codex_unhandled
      message: >-
        Codex failed during implementation. See the original failure and
        implementation artifacts for diagnosis.
      cause: "${{ failure }}"
```

This is useful for container-level catches that add domain context without
recovering.

## Retry Semantics

Retry is the existing node-local scalar retry property. It is not a catch action
and does not need a new object-shaped syntax for failure handling.

Example:

```yaml
call_service:
  op: command_execution
  retry: 3
  inputs:
    timeout: 30s
    run: ./scripts/call-service.sh
  catch:
    - id: validation_error_goes_to_review
      when: >-
        failure.kind == "task_error" &&
        failure_has_code(failure, "validation_failed")
      to: user_review
      payload:
        failure_message: "${{ failure.message }}"
```

In this example, `retry: 3` keeps its existing recipe meaning: three retries
after the first failed attempt.

Retry ordering:

1. The node attempt fails.
2. The node's `catch:` clauses evaluate first.
3. If a catch clause uses `to:` or `continue:`, the failure is handled and no
   retry is scheduled.
4. If no catch clause matches, or a catch clause uses `fail:`, the resulting
   failure is considered unhandled by catch.
5. The node's existing retry count is checked against that unhandled failure.
6. If retry attempts remain, the same node invocation is attempted again.
7. If retry is not configured or attempts are exhausted, the failure propagates
   to the parent node.

This ordering repeats for each failed attempt. Every retry attempt gets the same
local catch-first behavior before another retry decision is made.

This ordering makes catch more specific than retry. Authors can catch known
business failures and route them immediately, while allowing uncaught failures
to use the existing retry behavior.

If a catch clause wants to annotate a failure but still allow retry, it should
use `fail:`:

```yaml
deploy:
  op: git+https://github.com/colony-2/c2ops.git//gha@main
  retry: 2
  inputs:
    workflow: deploy.yml
  catch:
    - id: annotate_deploy_system_error
      when: 'failure.kind == "system_error"'
      fail:
        kind: system_error
        code: deploy_workflow_system_error
        message: "Deploy workflow failed before completion."
        cause: "${{ failure }}"
```

The retry attempt should use the same node invocation inputs, vars, artifacts,
and transition payload as the failed attempt. Previous attempts should be
available through the node's normal run history (`.runs[]`) and through retry
diagnostics in the job story.

## Propagation Rules

Recommended authoring semantics:

1. A node runs normally.
2. If it succeeds, `catch:` is ignored and normal `transitions:` or sequence
   ordering apply.
3. If it fails, the node's own `catch:` clauses are evaluated in order.
4. If a clause handles the failure with `to` or `continue`, the failure is
   considered caught.
5. `to` routes directly to the target state. `continue` completes the failed
   node with substitute outputs, then resumes the normal parent behavior for a
   completed node.
6. If a clause uses `fail`, or no clause matches, the node's existing retry
   count is checked against the unhandled failure.
7. If retry schedules another attempt, the same node invocation runs again.
8. If retry is absent or has no attempts remaining, the failure
   propagates to the parent node.
9. Parent catches see the propagated `failure`, including `failure.cause` for
   nested failures.
10. If the failure reaches the recipe root without being caught, the job fails as
   it does today.

This keeps catch behavior local and explicit. The nearest matching handler wins,
and broader parent handlers act as fallbacks.

## Example: Timeout Takes An Alternative Path

This pattern fits the existing `new-ticket.yaml` implementation state.

```yaml
implement:
  op: git+https://github.com/colony-2/c2ops.git//codex@main
  transitions:
    - to: pre_implementation_followup_review
      when: 'has(outputs.incompleteCategory) && outputs.incompleteCategory == "needs_user_input"'
    - to: validate
      when: "true"
  catch:
    - id: codex_timeout
      when: 'failure.kind == "timeout"'
      to: pre_implementation_followup_review
      payload:
        reason: codex_timeout
        guidance: >-
          The implementation op timed out. Review any implementation artifacts,
          then decide whether to resume the same session or revise the plan.
        failure_message: "${{ failure.message }}"
```

The existing success path is unchanged. Only true runtime timeout failures enter
the catch path.

## Example: System Error Routes To A Retry State

Recipes can model retries with ordinary states instead of requiring special
retry syntax when the recovery path needs explicit workflow state or human
review.

```yaml
state:
  initial: implement
  states:
    implement:
      op: git+https://github.com/colony-2/c2ops.git//codex@main
      transitions:
        - to: validate
          when: "true"
      catch:
        - id: transient_system_error
          when: 'failure.kind == "system_error" && failure.retryable == true'
          to: implementation_retry_wait
          payload:
            failed_state: implement
            failure_message: "${{ failure.message }}"

    implementation_retry_wait:
      op: sleep
      transitions:
        - to: implement
          when: "true"
      inputs:
        duration: 30s
```

If attempt limits are needed, the recipe can add an explicit counter state or
use prior state run counts.

## Example: Retry After Unhandled Catch Failure

This pattern uses the existing scalar retry count, but routes known task failures
to a review state before retry can run.

```yaml
run_ci:
  op: git+https://github.com/colony-2/c2ops.git//gha@main
  retry: 3
  inputs:
    workflow: ci.yml
    timeout: 20m
  catch:
    - id: workflow_concluded_failure
      when: 'failure.kind == "task_error" && failure.task.status == "failure"'
      to: ready_to_merge_review
      payload:
        validation_failed: true
        failure_message: "${{ failure.message }}"
```

If `gha` fails because the workflow concluded with failure, the catch routes to
review and retry does not run. If `gha` fails and no catch handles it, the
existing retry count is used before the failure propagates.

## Example: Specific Task Error String

String matching should be a fallback for ops that do not expose structured
codes.

```yaml
outcome_determination:
  op: recipe.run_and_get_result
  vars:
    user_feedback: '${{ transition.?payload.user_feedback.orValue("") }}'
  inputs:
    name: new-ticket-outcome-determination
    inputs:
      user_feedback: "${{ vars.user_feedback }}"
  transitions:
    - to: pre_implementation_review
      when: "true"
  catch:
    - id: malformed_plan_contract
      when: >-
        failure.kind == "task_error" &&
        failure_message_contains(failure, "Final response must be ONLY")
      to: outcome_determination
      payload:
        user_feedback: >-
          The outcome author failed the required JSON-only response contract.
          Regenerate the outcome bundle and strictly return plan.json content.
```

For durable recipes, prefer a stable `failure.code` such as
`response_schema_validation_failed` when the op can provide it.

## Example: State-Machine Fallback Catch

A state machine can have a broad catch that routes any otherwise-unhandled
failure to a review state.

```yaml
state:
  initial: requirements_planning
  states:
    requirements_planning:
      op: recipe.run_and_get_result
      inputs:
        name: new-ticket-requirements-planning
      transitions:
        - to: implementation_planning
          when: "true"

    implementation_planning:
      op: recipe.run_and_get_result
      inputs:
        name: new-ticket-implementation-planning
      transitions:
        - to: outcome_determination
          when: "true"

    failure_review:
      op: input
      inputs:
        form:
          title: "Workflow failure review"
          fields:
            - id: decision
              type: multiple_choice
              question: |
                A workflow node failed.

                Failed node: ${{ transition.failure.node.path }}
                Failure kind: ${{ transition.failure.kind }}
                Message: ${{ transition.failure.message }}
              required: true
              options:
                - value: retry_requirements
                  label: Retry requirements
                - value: cancel_job
                  label: Cancel job
      transitions:
        switch: outputs.fields.decision
        cases:
          - value: retry_requirements
            to: requirements_planning
          - value: cancel_job
            to: cancel_job_route

catch:
  - id: otherwise_unhandled_task_failure
    when: 'failure.kind == "task_error"'
    to: failure_review
    payload:
      source: state_machine_fallback
```

This gives the primary orchestration job a final domain-aware fallback while
allowing narrower states to catch more specific failures first.

## Example: Child Recipe Converts Failure To Structured Output

Child recipes often need to report a recoverable failure to a parent recipe
instead of failing the parent job.

```yaml
sequence:
  - id: detect
    op: command_execution
    inputs:
      timeout: 15m
      run: c2j cells --json

catch:
  - id: detect_timeout
    when: 'failure.kind == "timeout"'
    continue:
      outputs:
        status: timed_out
        passed: false
        suggested_commands: ""
        failure_message: "${{ failure.message }}"

outputs:
  status: ok
  passed: true
  suggested_commands: "${{ sequence.detect.outputs.stdout }}"
```

If a container-level `continue:` catch runs, its outputs become the container
outputs for that invocation. The normal success `outputs:` mapping is used only
when the container completes without taking a catch path.

## Authoring Guidance

- Use normal outputs and transitions for expected business outcomes.
- Use `catch:` for failures that would otherwise abort the node.
- Put specific catches closest to the node that understands the failure.
- Put broad fallback catches on the enclosing state machine or recipe root.
- Order catches from most specific to broadest, just like transitions.
- Prefer `failure.kind` and `failure.code` over message matching.
- Treat `message_contains` and `message_matches` as compatibility bridges for
  legacy ops or external tools without structured errors.
- Do not catch authoring errors, invalid recipe YAML, schema validation errors,
  or unsafe template render errors unless the runtime has already entered a
  well-defined node execution.
- Do not catch external cancellation by default in primary jobs. Cancellation
  usually means the job should stop.
- When a catch path needs human review, pass a concise failure summary in
  `payload` and keep diagnostic artifacts available.

## Recommended Initial Feature Set

A small first version should be enough for current recipe needs:

1. `catch:` on state-machine states.
2. `catch:` on top-level state-machine nodes.
3. `when:` CEL with `failure` in scope.
4. Existing node-scoped values available in catch conditions and action
   templates.
5. Helper functions for message text, codes, origin node, and root cause.
6. `to:` plus `payload:` for state-machine recovery paths.
7. `continue:` for child recipes that want to expose structured failure outputs.
8. Existing node-local `retry:` count checked after unhandled catch failures.

Using `continue:` on individual failed sequence steps can come next. It is
powerful but needs careful authoring guidance because substitute outputs can
hide missing data.

## Documentation Updates Needed

If this pattern is adopted, update:

- `guides/STATE_MACHINE_GUIDE.md` with success transitions versus failure
  catches.
- `guides/SEQUENCE_GUIDE.md` with `continue:` examples.
- `guides/NODE_SCOPE_SPEC.md` with `failure` and `transition.failure` scope.
- `guides/TEMPLATE_REFERENCE_CHEATSHEET.md` with failure helper functions.
- Retry documentation with catch-before-existing-retry ordering and attempt
  history.
- Per-op guides with stable `failure.code` values where possible.

## Open Questions

- Should cancellation be catchable only with an explicit
  `catch_cancellation: true` opt-in?
- Should catch-continued outputs be visible to job-story assertions through a
  dedicated namespace, or only as the node's final outputs?
- Should `failure.artifacts` be exposed directly in catch payloads, or only
  through the failed node's normal artifact namespace when artifacts exist?
- Should attempt counts be a first-class `failure.attempt` field, or should
  recipes use existing `.runs[]` data?
- Should retry exhaustion annotate the final propagated failure?
- Should recipe tests assert catch decisions with a dedicated assertion type,
  similar to `transition_payload_equals`?
