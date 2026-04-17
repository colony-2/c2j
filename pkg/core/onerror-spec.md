# OnError semantics for recipe nodes

Date: 2026-01-29

## Goal

Add configurable error-handling semantics to every recipe node (ops, sequences, states) so authors can choose behaviours beyond "retry then fail job". Also surface per-run outcomes in contextual outputs for downstream steps and UIs.

## API additions

### NodeMetadata
- New field `on_error` with shape:
  ```yaml
  on_error:
    action: <OnErrorAction>
  ```
- `OnErrorAction` enum (string):
  - `FAIL_AFTER_RETRIES` (default) – current behaviour.
  - `FAIL_IMMEDIATELY` – abort retries and fail the node on first error.
  - `IGNORE_AFTER_RETRIES` – exhaust retries, then mark node successful and continue; failure details recorded in outputs.
  - `IGNORE_IMMEDIATELY` – skip retries, mark node successful, continue; failure details recorded in outputs.
- If `on_error` is nil or `action` empty, default to `FAIL_AFTER_RETRIES`.

### Contextual outputs
Augment `StepOutput` / `RunOutput` (sequence, state machine, op scopes) with an `outcome` object so downstream steps can branch on success/failure.

```yaml
outcome:
  status: success | failure
  error: # only present on failure
    message: string
    type: string            # optional error class/type name
    details: object         # optional structured payload (e.g., serialized activity error)
    stacktrace: string      # optional truncated stack trace
```

- `status` is `success` for normal completion or any IGNORE* action that chooses to proceed.
- `status` is `failure` when the node surfaces an error and the action is FAIL_*.
- When IGNORE*, `status` is still `success` but `error` is populated so callers can inspect.
- `RunOutput` mirrors `StepOutput` and captures the outcome per retry/loop iteration.

## Runtime semantics

### Ops
1) Execute with existing retry policy (unless IGNORE_IMMEDIATELY or FAIL_IMMEDIATELY short-circuits retries).
2) On failure:
   - `FAIL_AFTER_RETRIES` (default): retry per policy; if still failing, propagate error and set outcome.status=failure.
   - `FAIL_IMMEDIATELY`: do not retry; propagate error, outcome.status=failure.
   - `IGNORE_AFTER_RETRIES`: retry per policy; if still failing, **do not** return error; record outcome.status=success with `error` filled; continue workflow.
   - `IGNORE_IMMEDIATELY`: skip retries; record outcome.status=success with `error` filled; continue workflow.
3) Successful run sets outcome.status=success and clears error.

### Sequences
- Apply `on_error` on the sequence node itself (wrapping execution of child nodes).
- Sequence failure handling applies to the failing child’s error result; IGNORE* actions allow the sequence to continue to next sibling.
- When sequence continues after ignoring, `StepOutput` for the failed child records outcome/error; subsequent steps can branch using `sequence.<id>.outcome`.

### State machines
- Apply `on_error` at the state (node) level.
- FAIL_*: bubble error, halting the machine.
- IGNORE_*: mark the state as successful with outcome.error populated, and proceed to transition evaluation.
- Transition CEL may inspect `states.<state>.outcome` to branch on failure/success.

## Compiler / worker changes (implementation notes)

- Inject default `OnErrorFailAfterRetries` when `metadata.OnError` is nil or action empty.
- Wrap op execution loop to honour `OnErrorAction`, deciding whether to retry, short-circuit, or suppress errors.
- When suppressing, construct the `error` payload from the activity error (message, type, details if available).
- Extend `ResolutionContext.AddExecutionWithArtifacts` to store `outcome` on `StepOutput`/`RunOutput`.
- Seed placeholder outputs with `outcome.status: "success"` and empty error so validation mode still succeeds.
- Update schema generation (JSON Schema/OpenAPI) to include `on_error` and the new `outcome` fields.

## Open questions / edge cases

- Should `IGNORE_*` propagate artifacts produced before failure? (Proposal: include any artifacts returned alongside the error; if none, keep existing artifacts map empty.)
- Do we need a `RETRY_ONCE_THEN_IGNORE` variant? (Not in v1; retry policy can approximate.)
- How to map temporal/cadence error types into `error.type`? (Proposal: use the activity error name; for generic errors, use Go type.)
- Should `FAIL_IMMEDIATELY` still count as one attempt in RunOutputs? (Yes; record a single run with outcome failure.)

## Validation & tooling

- Update `schema.json` and `oas.json` via `GenerateSchemaString` to expose `on_error`.
- Extend compiler validation helpers to seed `outcome` in placeholders.
- Add unit tests for op, sequence, and state-machine paths covering each `OnErrorAction`.

