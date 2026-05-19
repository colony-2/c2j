# Requirement 2: State-Aware Lookup Helpers

## Does The Spirit Make Sense?

Yes. Reading prior states is a first-class recipe pattern, and making every recipe manually prove that a state has run is noisy and error-prone.

## Proposal

- Add `state_exists(id)`, `state_output(id, path, default)`, and `state_field(id, field, default)` as resolver-aware CEL functions.
- Implement them against the current `ResolutionContext.TemplateData.States` map so they respect the visible state-machine scope.
- Define `state_exists(id)` as true only when the state has completed at least once in the current visible state scope. Declared-but-not-run states return false, including validation placeholders.
- Keep `state_field(id, field, default)` as a single-field helper for `states.<id>.outputs.fields.<field>`. It should not accept nested field paths.
- Keep prior-run reads as direct `states.<id>.runs[n]` CEL access rather than adding dedicated run helpers initially.
- Define the `id` as the visible state key used by `states.<id>`, which is currently `ScopeID(state metadata, state map key, ScopeState)`.
- Ensure validation placeholders do not make `state_exists(...)` appear true during compile/validate mode.
- Provide Go-template equivalents if the function registry keeps CEL and Go-template functions paired.

## Risks

- State re-entry currently stores latest outputs at `states.<id>.outputs` and older runs under `runs`. The helper must document that it reads the latest completed invocation.
- State names and explicit state node IDs can diverge. Helpers must use one canonical lookup key.
- Validation mode seeds future-state placeholders, which can accidentally blur "declared" versus "completed".

## Clarifying Questions

Resolved.

## Decisions

- `state_exists(id)` means the state has completed at least once in the current visible state scope.
- Prior-run reads stay as direct `states.<id>.runs[n]` CEL access for now.
- Keep `state_field(id, field, default)` and support a single form field ID only.
