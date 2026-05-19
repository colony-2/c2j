# Requirement 5: Switch/Table Transitions

## Does The Spirit Make Sense?

Mostly yes, but the proposed YAML shape needs care. A compact routing table is useful, but preserving ordered transition semantics is non-negotiable.

## Proposal

- Keep the existing list-style `transitions` as the canonical execution model.
- Add a parser/normalizer that accepts either:
  - existing `transitions: [...]`, or
  - `transitions: { switch, cases, default }`, where `cases` is an ordered list with explicit `value` fields.
- Normalize switch transitions into an ordered list of ordinary transitions before execution and story/test diagnostics.
- Preserve original switch/case metadata for diagnostics while also exposing the expanded CEL expression used for deterministic execution.
- Preserve case order directly from the list. Duplicate case values should be rejected when statically visible.
- Define `default` as running when no case fully matches. A case with a `when` guard matches only when both the switch value and guard match.
- Allow a case to contain either `to` or a nested `switch`, but not both. Nested switch branches must eventually select a `to`.
- Support one nested switch level initially by expanding nested cases into conjunctions of parent and child case expressions.
- Emit duplicate-case errors during YAML decode/normalization when visible from the YAML node.

## Risks

- Mapping-style cases make order and duplicate detection easy to lose if decoded into maps too early, so the accepted switch syntax should avoid map-style cases.
- YAML case keys can be strings, numbers, or booleans; generated CEL equality must quote and type them correctly.
- Static unreachable-case detection is limited once cases have extra `when` guards.
- Nested default semantics can be surprising unless specified exactly.

## Clarifying Questions

Resolved.

## Decisions

- Use ordered-list switch cases with explicit `value` fields. Do not use mapping-style cases for the initial implementation.
- Switch `default` runs when no case fully matches, including optional case guards.
- A switch case may contain either `to` or a nested `switch`, but not both. Nested switch branches must eventually select a target state.
- Job stories and test diagnostics should show both the original switch/case structure and the expanded CEL transition expression.
