# Plan 1A: CEL Null-Safety Profile for State Access

## Goal

Reduce repetitive guard expressions around `states.*` access while preserving backward compatibility.

## Problem pattern in current recipes

Recipes repeatedly use structures like:

- `"<state>" in states && has(states.<state>.outputs.fields.x) ? states.<state>.outputs.fields.x : ""`
- Nested fallbacks across multiple states with large ternary chains.

This makes authoring and reviewing error-prone.

## Proposal

Add an optional CEL execution profile where missing keys/fields evaluate to `null` instead of raising evaluation errors.

### New profile knob

At recipe root:

```yaml
template_options:
  cel_profile: strict | null_safe
```

- `strict` (default): current behavior.
- `null_safe`: missing map key / missing field / null dereference returns `null`.

### Optional operators/helpers (if needed)

- `coalesce(a, b, c)` for fallback chains.
- Optional null-safe operator support (`?.`, `??`) if CEL integration allows it without grammar risk.

## Recipe changes expected

### Before

```yaml
${{ "pre_implementation_review" in states && has(states.pre_implementation_review.outputs.fields.feedback) ? states.pre_implementation_review.outputs.fields.feedback : "" }}
```

### After (`null_safe`)

```yaml
${{ coalesce(states.pre_implementation_review.outputs.fields.feedback, "") }}
```

### Impacted recipes

- `new-ticket.yaml`: outputs and transition conditions with deep state checks.
- `new-ticket-requirements-planning.yaml`, `new-ticket-implementation-planning.yaml`, `new-ticket-outcome-determination.yaml`: less defensive extraction logic.

## Engine/runtime changes

1. Update CEL adapter to map missing-field errors to `null` only in `null_safe` profile.
2. Keep strict compile/runtime behavior unchanged for existing recipes.
3. Add linter warning for redundant guards in `null_safe` recipes.

## Migration plan

1. Implement profile flag + tests (strict unchanged).
2. Add one pilot conversion for `new-ticket.yaml`.
3. Add recipe formatter/linter suggestion for simplification.
4. Expand migration to remaining recipes if pilot readability and reliability improve.

## Compatibility and risks

- Compatibility: high (strict default).
- Risk: silent null propagation can hide misspelled field names.
- Mitigation: lint rule `unknown_path_in_null_safe` with warning/error mode.

## Success criteria

1. `new-ticket.yaml` expression lines reduced by at least 30%.
2. No regression in `run-tests.sh`.
3. Reviewers report faster comprehension of state logic.
