# Recipe Authoring Ergonomics Feature Summary

This document summarizes the new recipe authoring primitives added for safer,
shorter, and more reviewable recipes.

## Safe Access And Defaulting

- CEL optional access is enabled, so authors can safely read optional paths with
  syntax such as `states.?review.outputs.fields.feedback.orValue("")`.
- `nonempty(value)` returns true when a value exists and is not empty.
- `first_nonempty(values...)` returns the first non-empty value, preserving the
  original value. If no value qualifies, it returns null.
- `first_nonempty` trims strings only for the emptiness check. It does not trim
  the returned value.
- The helpers are available in both CEL expressions and Go-template rendering.

Example:

```yaml
title: >-
  ${{
    first_nonempty(
      inputs.?override_title,
      states.?draft.outputs.title,
      "Untitled"
    )
  }}
```

## State Lookup Helpers

Recipes can read previous state outputs without hand-written existence checks:

- `state_exists(id)` returns whether a real state invocation has completed.
- `state_output(id, path, default)` reads from `states.<id>.outputs`.
- `state_field(id, field, default)` reads one form field from
  `states.<id>.outputs.fields`.

Example:

```yaml
commit_message: >-
  ${{
    state_field("ready_to_merge_review", "commit_message", "")
  }}
```

## Scoped Vars

Recipes can define computed `vars` at recipe, sequence, state-machine, state,
and op scopes.

Rules:

- Vars may reference only outer-scope data and outer vars.
- Vars in the same `vars` block may not reference each other.
- Child scopes inherit a snapshot of outer vars.
- Inner scope vars are re-evaluated each time that scope is entered.
- Op-level vars are resolved immediately before the op inputs are resolved.

Example:

```yaml
vars:
  selected_title: '${{ first_nonempty(inputs.title, "Untitled") }}'

sequence:
  - id: draft
    op: write_doc
    vars:
      final_title: '${{ vars.selected_title + " draft" }}'
    inputs:
      title: '${{ vars.final_title }}'
```

## Transition Payloads

State-machine transitions can pass structured payloads to the target state.

- `transition.payload` is visible only to the state invocation reached by that
  transition.
- `transition.from` and `transition.to` are available as built-in metadata.
- Transition payload expressions are rendered in the source state's scope.
- `outputs` is available while evaluating transition conditions and payloads.
- Initial transitions can also include payloads.
- Payload render failures fail before the target state starts and include
  transition context in the error.

Example:

```yaml
review:
  op: input
  inputs:
    form:
      fields:
        - id: decision
          type: dropdown
          question: Decision?
          options:
            - value: revise_requirements
        - id: feedback
          type: paragraph_text
          question: Feedback
  transitions:
    - to: requirements
      when: outputs.fields.decision == "revise_requirements"
      payload:
        user_feedback: '${{ outputs.fields.feedback }}'
        reason: revise_requirements

requirements:
  op: plan_requirements
  inputs:
    user_feedback: '${{ transition.?payload.user_feedback.orValue("") }}'
```

## Switch/Table Transitions

Transitions can be written as a deterministic switch table and are normalized
into ordinary ordered transitions.

Supported shape:

```yaml
transitions:
  switch: outputs.fields.decision
  cases:
    - value: approve
      to: done
    - value: revise
      to: requirements
      payload:
        user_feedback: '${{ outputs.fields.feedback }}'
  default:
    to: fallback
```

Rules:

- Cases are ordered and use explicit `value` fields.
- Duplicate visible values are rejected.
- A case can have `to` or a nested `switch`, but not both.
- One nested switch level is supported.
- A `default` branch runs when no case fully matches.
- Case/default branches can carry transition payloads.

## Input Form Output Defaults

Successful `input` ops now emit stable `outputs.fields` based on the declared
form schema.

Defaults:

- `short_answer`, `paragraph_text`, `date`, `time`: `""`
- `multiple_choice`, `dropdown`: `""`
- `checkboxes`: `[]`
- `boolean`: `false`
- `linear_scale`: configured `scale.min`
- explicit `default` overrides the type default

Single-question input now populates both:

- `outputs.response`
- `outputs.fields.response`

Required fields must be submitted or have an explicit default. Recipe-test mocks
that omit required fields fail instead of silently inventing values.

## Review Feedback Selection Pattern

There is no implicit winner across prior review states. The recommended pattern
is to carry target-specific feedback on the transition that routes to the target
state.

Use:

```yaml
user_feedback: '${{ transition.?payload.user_feedback.orValue("") }}'
```

Avoid scanning every previous review state to infer which feedback applies.
State lookup helpers remain useful for compatibility and explicit fallback
logic, but fallback must be authored directly.

## Testing And Diagnostics

Recipe-test and story output now expose the new primitives more directly.

Recipe-test additions:

- Compile validation now runs semantic validation where possible, including
  helper names, vars, transition payload expressions, and switch structure.
- Runtime diagnostics include rendered vars by scope/node path.
- Runtime diagnostics include transition evaluations and selected payloads.
- New assertions:
  - `var_equals`
  - `transition_payload_equals`

Example assertions:

```yaml
assertions:
  - type: var_equals
    scope: recipe
    path: selected_title
    value: "Release notes"

  - type: transition_payload_equals
    from_state: review
    to_state: requirements
    path: user_feedback
    value: "Clarify acceptance criteria"
```

Job stories now include redacted rendered vars and selected transition payloads.

## Redaction

Rendered vars and transition payload diagnostics are redacted by default when:

- the key/path looks sensitive, such as `password`, `token`, `secret`,
  `api_key`, or `private_key`;
- the value matches common secret-looking patterns;
- the value looks like a high-entropy token.

Redaction is best-effort diagnostics hygiene, not a security boundary.

## Compatibility Notes

- Input outputs now include defaulted optional fields, which changes observable
  `outputs.fields` for existing recipes.
- Required field omissions in recipe-test mocks can now fail.
- Review feedback selection intentionally has no implicit fallback or winner.
- Authors should use optional access or state lookup helpers for missing states,
  and transition payloads for target-specific routing context.
