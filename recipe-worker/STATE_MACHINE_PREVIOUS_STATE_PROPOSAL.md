# Proposal: expose previous state context inside state machines

## Goal
Make the name (and basic metadata) of the previously executed state available to:
- CEL/Go-template resolution inside each state execution (inputs, when clauses, op parameters, etc.).
- State machine–level outputs resolved after the machine finishes.
- Validation mode as well as normal execution.

## Current behavior (observed in code)
- `executeStateMachine` in `pkg/compiler/statemachine_compiler.go` tracks `currentState` only; prior state is not recorded.
- `template.ResolutionContext` exposes `inputs`, `sequence`, `states`, `scope`, and `context` to CEL. `scope` (`ScopeMetadata`) currently only holds `ExecutionID` and `Timestamp` and is freshly initialized per scope.
- State contexts (`ScopeState`) copy `States` and `ContainerInputs` from the state-machine parent, but not any notion of execution order. The `states` map can’t reliably tell “what ran most recently” because it is unordered and optionally pre-seeded with placeholders (`seedStateMachinePlaceholders`).
- State-machine outputs are resolved in the parent state-machine context (`resCtx.ResolveMap(outputTemplate)`), so any new variable must also live there.

## Proposed shape and API
Add a small, immutable snapshot of state-machine position to `scope`:
```json
scope.state_machine: {
  current: string,   // name being executed right now
  previous: string,  // name executed immediately before current; "" on first state
  initial: string,   // initial state name for reference
  run: int           // 0-based index of current execution (advances on each state entry)
}
```
This appears to CEL users as `scope.state_machine.previous`. In templates: `{{ scope.state_machine.previous }}`.

Rationale:
- Lives under `scope` to mirror other execution metadata and avoid polluting `inputs`/`states`.
- Strings keep the API simple; callers already use `states[...]` to fetch outputs/artifacts when needed.
- `run` allows disambiguating loops (same state executed multiple times).

## Where to populate it
1. **State-machine loop (`executeStateMachine`)**
   - Track `previousState` (empty string initially) and `runIndex` (0-based).
   - Before calling `runState`, set on the parent `resCtx.TemplateData.Scope.StateMachine`:
     - `current = currentState`, `previous = previousState`, `initial = stateMap.Initial`, `run = runIndex`.
   - After the state finishes (before transition evaluation), update `previousState = currentState` and increment `runIndex`.
   - Ensure the final setting is kept so `resolvedOutputs` sees the last/current/previous values.

2. **State child context (`runState`)**
   - After `NewChildContext(ScopeState, ...)`, copy the parent’s `Scope.StateMachine` struct onto the child so CEL in state inputs/ops can read it.
   - Because `ScopeMetadata` is a struct, a shallow copy suffices; avoid sharing pointers.

3. **Validation mode (`ValidateAll`)**
   - While iterating sorted states, set the same `Scope.StateMachine` fields before each `runState` call so validation sees consistent data.

## Edge cases and semantics
- **First state:** `previous` is `""` and `run = 0`; `current = initial`.
- **Loops / re-entrance:** `current` reflects the state being (re)run; `previous` is the one that led to it (could be the same name if we loop). `run` increments each time any state starts executing.
- **No transitions matched (terminal):** The final `scope.state_machine` snapshot remains set to the last executed state; outputs can inspect both `current` (the terminal state) and `previous` (what ran before it).
- **Placeholders / AllowFutureStepRefs:** Unchanged; `scope` carries ordering so users don’t rely on map order in `states`.

## Compatibility and safety
- Additive struct change to `ScopeMetadata`; existing CEL that ignores the new field remains valid.
- No change to how `states[...]` is stored; only metadata is added.
- Keep JSON tags on new fields to preserve serialization semantics if contexts are logged or persisted.

## Testing ideas
- Unit tests in `server/recipe-template/pkg/template` for CEL evaluation showing `scope.state_machine.previous` inside a state and in final outputs.
- Integration test in `recipe-worker` running a tiny two-state machine with a loop to verify `current/previous/run` values captured across iterations.
- Validation-mode test to confirm the field is set during `ValidateAll` traversal.

## Migration notes
- Document the new variable in `server/recipe-template` docs (e.g., `cel_jq_spec.md` or a short section in README) to keep template authors aware of the shape and semantics.

