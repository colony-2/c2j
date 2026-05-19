# Requirements: Recipe Authoring Ergonomics

## Status

Draft requirements for c2j and recipe-system improvements.

## Motivation

Current state-machine recipes can become hard to read when they need to safely
pull optional values from prior states, especially review/input states.

For example, `new-ticket.yaml` has to repeatedly express logic like:

```cel
("pre_implementation_review" in states &&
  has(states.pre_implementation_review.outputs.fields.feedback) &&
  states.pre_implementation_review.outputs.fields.feedback != "" &&
  has(states.pre_implementation_review.outputs.fields.decision) &&
  (
    states.pre_implementation_review.outputs.fields.decision == "back_up_requirements" ||
    (
      states.pre_implementation_review.outputs.fields.decision == "revise_current_stage" &&
      has(states.pre_implementation_review.outputs.fields.target_stage) &&
      states.pre_implementation_review.outputs.fields.target_stage == "requirements"
    )
  )
)
  ? states.pre_implementation_review.outputs.fields.feedback
  : ...
```

The recipe author is trying to say something simple: "if the prior review sent
the workflow back to requirements with feedback, pass that feedback to the
requirements recipe." The current expression forces the author to manually
encode missing-state checks, missing-field checks, string non-empty checks,
decision matching, and fallback ordering.

## Goals

- Make common recipe expressions short enough to review confidently.
- Preserve explicit, deterministic recipe behavior.
- Avoid hiding routing behavior in opaque code scripts.
- Keep recipe tests able to compile, run, and assert the expanded behavior.
- Improve job stories so humans can see which feedback or override value was
  selected and why.

## Non-Goals

- Do not replace CEL as the recipe expression language.
- Do not make missing required data silently pass validation.
- Do not make transition order ambiguous.
- Do not require recipes to adopt a new syntax all at once.

## Requirement 1: Safe Path Access And Defaulting Helpers

c2j should provide helper functions, available anywhere recipe expressions are
evaluated, for safe lookup and defaulting.

Minimum required helpers:

- `get(value, path, default)` returns `default` when any path segment is absent
  or null.
- `present(value)` returns true when a value exists and is not null.
- `nonempty(value)` returns true for non-empty strings, lists, and maps.
- `coalesce(values...)` returns the first present value.
- `coalesce_nonempty(values...)` returns the first present, non-empty value.

The `path` argument should support nested map/object traversal without requiring
recipe authors to mix `in`, `has(...)`, and direct dereferences. Either dotted
strings or segment lists are acceptable, but the chosen form must be documented
and testable.

Example:

```yaml
upstream_repo: >-
  ${{
    coalesce_nonempty(
      get(states, "ready_to_merge_review.outputs.fields.upstream_repo", ""),
      inputs.upstream_repo,
      context.git.repo,
      ""
    )
  }}
```

The helper must not mask type errors after a value is selected. If a field
expects a string and the selected value is a map, validation should still fail.

## Requirement 2: State-Aware Lookup Helpers

c2j should provide helpers for the common pattern of reading prior state outputs.

Minimum required helpers:

- `state_exists(id)` returns whether the state has completed at least once in
  the current visible state scope.
- `state_output(id, path, default)` safely reads from `states.<id>.outputs`.
- `state_field(id, field, default)` safely reads from
  `states.<id>.outputs.fields.<field>`.

Example:

```yaml
commit_message: >-
  ${{
    coalesce_nonempty(
      state_field("ready_to_merge_review", "commit_message", ""),
      inputs.title,
      "Update"
    )
  }}
```

These helpers should be defined for both state-machine recipes and recipe-test
execution. They should return defaults when the state has not run, rather than
raising a missing-key error.

## Requirement 3: Named Computed Values

Recipes should support named computed values, scoped similarly to inputs and
outputs, so repeated expressions can be authored once and referenced many times.

Required surface:

```yaml
vars:
  selected_title: '${{ coalesce_nonempty(inputs.title, "") }}'
  selected_description: '${{ coalesce_nonempty(inputs.description, "") }}'

state:
  states:
    requirements_planning:
      vars:
        user_feedback: >-
          ${{
            coalesce_nonempty(
              transition.payload.user_feedback,
              state_field("ready_to_merge_review", "feedback", ""),
              ""
            )
          }}
      inputs:
        inputs:
          title: "${{ vars.selected_title }}"
          description: "${{ vars.selected_description }}"
          user_feedback: "${{ vars.user_feedback }}"
```

Requirements:

- `vars` may be declared at recipe, sequence node, and state levels.
- State-local vars may reference recipe-level vars and the same context that the
  state's inputs can reference.
- Vars must support typed values, not only strings.
- Vars must be acyclic. Cycles should fail compile/validate with a clear error.
- The rendered vars for an executed node should be visible in recipe-test output
  and job story diagnostics.

## Requirement 4: Transition Payloads

State-machine transitions should be able to pass a structured payload to the
next state invocation.

This lets the source state explain why it routed somewhere and what values the
target state should consume, instead of forcing the target state to inspect
every possible prior review state.

Required surface:

```yaml
pre_implementation_review:
  op: input
  transitions:
    - to: requirements_planning
      when: 'outputs.fields.decision == "back_up_requirements"'
      payload:
        user_feedback: "{{ outputs.fields.feedback }}"
        source_review: pre_implementation_review
        reason: back_up_requirements
```

The target state can then consume:

```yaml
requirements_planning:
  inputs:
    inputs:
      user_feedback: '${{ get(transition.payload, "user_feedback", "") }}'
```

Requirements:

- `transition.payload` must be available only to the state invocation reached by
  that transition.
- Payload values must be rendered using the source state's visible scope.
- Payloads must be persisted in the job story.
- Recipe tests must be able to assert transition payload values.
- Re-entering a state must use the payload from the transition that caused that
  specific invocation.
- If a state starts without a transition payload, `transition.payload` should be
  an empty map.

## Requirement 5: Switch/Table Transitions

Recipes should support a compact transition table for the common pattern of
routing on one value, while preserving ordered transition semantics.

Required surface:

```yaml
pre_implementation_review:
  op: input
  transitions:
    switch: outputs.fields.decision
    cases:
      cancel_job:
        to: cancel_job_route
      back_up_requirements:
        to: requirements_planning
        payload:
          user_feedback: "{{ outputs.fields.feedback }}"
      back_up_implementation_planning:
        to: implementation_planning
        payload:
          user_feedback: "{{ outputs.fields.feedback }}"
      revise_current_stage:
        switch: outputs.fields.target_stage
        cases:
          requirements:
            to: requirements_planning
            payload:
              user_feedback: "{{ outputs.fields.feedback }}"
          implementation_planning:
            to: implementation_planning
            payload:
              user_feedback: "{{ outputs.fields.feedback }}"
        default:
          to: outcome_determination
          payload:
            user_feedback: "{{ outputs.fields.feedback }}"
    default:
      to: implement
```

Requirements:

- The transition table must compile to deterministic ordered transition logic.
- Cases may include an additional `when` guard.
- Nested switches are allowed but should be limited to one nested level in the
  initial implementation.
- Compile errors must identify unreachable or duplicate cases when statically
  obvious.
- Existing list-style transitions remain fully supported.

## Requirement 6: Input Form Output Defaults

The `input` op should emit a stable `outputs.fields` object based on the declared
form schema.

Requirements:

- Every declared field id should exist in `outputs.fields` after a successful
  input op.
- Optional text fields default to `""`.
- Optional multi-select fields default to `[]`.
- Optional boolean fields default to `false`.
- Required fields should be guaranteed present after the op succeeds.
- Field defaults may be overridden explicitly in the form schema.

This lets recipes use:

```cel
outputs.fields.decision == "back_up_requirements"
```

instead of:

```cel
has(outputs.fields.decision) && outputs.fields.decision == "back_up_requirements"
```

State existence still matters when reading an old state, so this requirement
should be paired with the state-aware helpers above.

## Requirement 7: First-Class Review Feedback Selection

c2j should provide enough generic primitives to make review feedback selection
compact without adding workflow-specific hard-coded helpers.

The target authoring pattern should be:

```yaml
requirements_planning:
  inputs:
    inputs:
      user_feedback: >-
        ${{
          coalesce_nonempty(
            get(transition.payload, "user_feedback", ""),
            state_field("ready_to_merge_review", "feedback", ""),
            ""
          )
        }}
```

Recipes should not need to manually repeat:

- whether each possible review state has run
- whether `outputs.fields.feedback` exists
- whether feedback is non-empty
- whether the review state's decision points to this target
- which review state should win if multiple have run

Transition payloads should carry target-specific feedback whenever possible.
State-aware helpers should serve as compatibility and fallback tools.

## Requirement 8: Testing And Diagnostics

Every new authoring primitive must be covered by recipe-test functionality.

Requirements:

- `c2j test compile` validates helper names, var dependencies, transition
  payload expressions, and switch transition structure.
- `c2j test run` evaluates helpers, vars, payloads, and switch transitions with
  the same semantics as embedded recipe execution.
- Recipe-test assertions can inspect rendered vars and transition payloads.
- Job stories show the selected transition, transition payload, and rendered vars
  for the executed state.
- Error messages include the recipe path to the failing helper, var, or
  transition case.

## Suggested Implementation Priority

1. Safe access/default helpers and input form output defaults.
2. State-aware lookup helpers.
3. Named computed values.
4. Transition payloads.
5. Switch/table transitions.

The first two items reduce expression noise immediately. Named values reduce
duplication. Transition payloads remove the largest structural problem: target
states reconstructing routing intent by scanning prior review states.
