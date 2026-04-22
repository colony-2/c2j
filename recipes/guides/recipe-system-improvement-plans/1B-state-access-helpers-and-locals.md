# Plan 1B: State Access Helpers + Local Bindings (Strict CEL)

## Goal

Make recipes cleaner without changing CEL error semantics.

## Problem pattern in current recipes

State-driven logic repeats:

- existence checks (`"<state>" in states`)
- nested `has(...)` checks
- long fallback chains duplicated across transitions, inputs, and outputs.

## Proposal

Keep CEL strict, and add two constructs:

1. Helper functions for common state/path access.
2. `locals` blocks for reusable computed values.

## Proposed helper functions

- `state_exists("state_name") -> bool`
- `state_get("state.path.to.value", default)`
- `state_str("state.path", default_string)`
- `state_bool("state.path", default_bool)`
- `first_non_empty(list<string>) -> string`
- `first_non_null(list<any>) -> any`

Example:

```yaml
${{ state_str("pre_implementation_review.outputs.fields.feedback", "") }}
```

## Proposed `locals` blocks

Allow `locals` at recipe root and per-state scope:

```yaml
locals:
  pre_feedback: '${{ state_str("pre_implementation_review.outputs.fields.feedback", "") }}'
  followup_feedback: '${{ state_str("pre_implementation_followup_review.outputs.fields.feedback", "") }}'
  review_feedback: '${{ first_non_empty([locals.followup_feedback, locals.pre_feedback]) }}'
```

Then reuse `locals.review_feedback` throughout outputs/transitions.

## Recipe changes expected

### Direct improvements

- `new-ticket.yaml`
  - replace duplicated feedback fallback chains.
  - reduce repeated state/session checks in `implement_resume` input templates.
- other recipes using `json_parse(...assistantSummary)` can use locals to parse once and reuse.

### Style change

- Prefer local aliases over repeating deep paths in-line.

## Engine/runtime changes

1. Add helper CEL functions to runtime function map.
2. Add `locals` evaluation order:
   - evaluate after `inputs/context` available.
   - expose as `locals.*` to current scope.
3. Validate dependency cycles in locals.

## Migration plan

1. Ship helpers first.
2. Ship locals in experimental mode.
3. Convert `new-ticket.yaml` as reference migration.
4. Publish an authoring guide update with before/after examples.

## Compatibility and risks

- Compatibility: very high (no behavior change for existing expressions).
- Risk: helper misuse with invalid path strings.
- Mitigation: path validation + compile-time warnings.

## Success criteria

1. At least 25% reduction in repeated expression snippets in `new-ticket.yaml`.
2. No behavior change in existing recipe tests.
3. Lower diff size for future recipe edits touching state wiring.
