# c2-implementation-compat-review@v1

## Purpose

Verify the implementation plan preserves compatibility and has safe sequencing.

## Required inputs (`/src/inbox`)

- `implementation/plan.json`
- `implementation/index.md`
- `implementation/dependency-ticket-specs.json` (optional)

## Required outputs (`/src/outbox/implementation`)

- `compat-review.json`:
  - `ok` (bool)
  - `blocking_issues` (list)
  - `feedback` (list)

## Execution steps

1. Review sequence feasibility and rollback safety.
2. Confirm dependency ordering is complete for cross-cell prerequisites.
3. Reject any implied backward-incompatible rollout.
4. Emit clear, actionable blockers.

## Guardrails

- Block plans that depend on undefined ordering.
- Keep output machine-routable (`ok` boolean is authoritative).

