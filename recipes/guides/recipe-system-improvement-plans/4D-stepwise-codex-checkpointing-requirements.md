# Requirements: Stepwise Codex Skill Execution with C2 Checkpointing

## Objective

Support skill-driven inner loops while keeping explicit C2 checkpoints, with **a new Codex process per step** (no long-lived Codex worker).

## Confirmation of platform support

This pattern is supported by current `codex.exec` behavior:

1. `codex.exec` runs Codex **non-interactively** per invocation.
2. `codex.exec` accepts optional `sessionId` input.
3. `codex.exec` returns `sessionId` output for reuse on a later invocation.

Implication: we can start a fresh process each step and still continue the same Codex conversation by passing the returned `sessionId` into the next step.

References:

- `guides/ops/OP_CODEX_EXEC.md`
- `ticket-implement.yaml`
- `RECIPE_TEST_STATEMENTS.md` (TS-011, TS-012)

## Scope

### In scope

- Skill-oriented execution where each recipe state calls `codex.exec` once.
- Recipe-level checkpoint gating between Codex steps.
- Artifact-based handoff between steps.

### Out of scope

- Long-lived Codex daemons/processes.
- Passing critical downstream data via assistant summary text.

## Functional requirements

### FR-1: Step unit

Each `codex.exec` invocation must execute at most one checkpointable skill segment and then return.

### FR-2: Process lifecycle

Each step must run in a newly started Codex process. No process is kept alive between recipe states.

### FR-3: Session continuity across processes

When continuity is needed, next step must pass prior `sessionId` to `codex.exec`.

### FR-4: Checkpoint return contract

Each step must emit `/src/outbox/<phase>/latest-status.json` with:

- `status` (machine-routable)
- `completed_skill`
- `next_skill_candidates` (optional)
- `summary`

### FR-5: Artifact-first data flow

Downstream steps must consume artifacts from inbox/outbox handoff; they must not parse assistant summary for authoritative data.

### FR-6: Recipe-controlled checkpoints

Recipe state machine must gate progression at checkpoint states (for example: pre-implementation and ready-to-merge).

### FR-7: Minimal user input points

User input remains limited to:

1. pre-implementation checkpoint
2. ready-to-merge checkpoint
3. implementation clarification only when `status=needs_user_input`

### FR-8: Backward compatibility

No step may introduce backward-incompatible behavior. If blocked by dependency changes, emit dependency ticket specs and wait.

## Non-functional requirements

1. Deterministic routing from structured status fields.
2. Idempotent resume behavior when the same `sessionId` is retried.
3. Human-reviewable progress via artifacts (`progress.ndjson`, summaries).

## Execution model

1. Recipe calls `codex.exec` with skills + current artifacts.
2. Codex performs one skill segment and writes outbox artifacts.
3. `codex.exec` returns with `sessionId` and status artifacts.
4. Recipe evaluates status and either:
   - routes to checkpoint input,
   - routes to another Codex step using same `sessionId`,
   - or routes backward/cancel paths.

## Acceptance criteria

1. No recipe path depends on a long-lived Codex process.
2. Every Codex step can be resumed by launching a new process with prior `sessionId`.
3. Checkpoint decisions remain explicit in recipe graph.
4. All cross-step handoff data is artifact-backed.
5. Existing continue/new-session behavior remains valid.

## Optional hardening enhancement

If stronger enforcement is needed beyond prompt/skill contract, add an explicit `codex.exec` input like `return_on=skill_boundary` or `max_skill_segments=1`.
