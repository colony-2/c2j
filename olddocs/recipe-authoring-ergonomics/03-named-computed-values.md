# Requirement 3: Named Computed Values

## Does The Spirit Make Sense?

Yes, but this is a larger language feature than the helpers. It is justified because repeated expressions are hard to audit and because vars give tests and job stories a natural place to show "what value was selected."

## Proposal

- Add a `vars` map to recipe metadata and node metadata, making it available at recipe, sequence, state-machine, state, and op scopes. We can initially document support for recipe, sequence, and state scopes.
- Add `vars` to template/CEL data as a scoped map.
- Evaluate each `vars` block when entering its scope invocation. Vars are immutable after rendering for that invocation.
- During evaluation, a `vars` block may reference only outer-scope data and outer vars, not sibling vars from the same block.
- Child scopes inherit a snapshot copy of the rendered outer vars at the time the child scope is entered.
- Inner scopes evaluate their own `vars` each time they are entered. Re-entered states therefore recompute state-local vars for each invocation.
- Resolve vars before resolving the inputs that need them:
  - recipe vars after recipe inputs are validated/defaulted;
  - sequence/state-machine vars before executing child nodes;
  - state vars before resolving that state's node inputs;
  - op vars before resolving op inputs.
- Resolve vars as typed template values, not string-only interpolation.
- Reject same-block var references rather than supporting dependency graphs. This avoids cycle detection and ordering ambiguity.
- Record rendered vars in recipe-test results and job-story nodes, keyed by node path/scope.

## Risks

- Evaluation order changes are invasive for sequence and state-machine inputs, which are currently resolved before the child context exists.
- Disallowing same-block references means authors must either repeat a small expression, use an outer var, or introduce an inner sequence/state when they need staged derived values.
- Shadowing can become confusing if recipe-level and state-level vars share names.
- Vars may contain sensitive values; story/test output needs a redaction policy before exposing them broadly.

## Clarifying Questions

Resolved.

## Decisions

- Vars are evaluated once when entering each scope invocation and are immutable after rendering.
- Vars may reference only outer-scope data and outer vars, not sibling vars from the same `vars` block.
- Child scopes inherit a snapshot copy of rendered outer vars.
- Inner scopes re-evaluate their own vars each time they are entered; re-entered states recompute state-local vars for each invocation.
- Allow op-level vars. They are evaluated immediately before resolving that op's inputs.
