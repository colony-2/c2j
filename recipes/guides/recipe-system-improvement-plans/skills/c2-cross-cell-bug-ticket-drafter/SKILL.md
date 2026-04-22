# c2-cross-cell-bug-ticket-drafter@v1

## Purpose

Convert discovered cross-cell bugs into deterministic ticket specs suitable for `ticket.manage`.

## Required inputs (`/src/inbox`)

- `implementation/pending-bugs.json` or equivalent findings artifacts
- `context/cells.json`

## Required outputs (`/src/outbox/implementation`)

- `dependency-ticket-specs.json`:
  - each item includes `component`, `title`, `requestedChanges`, `depends_on`, `priority`, `evidence`.

## Execution steps

1. Normalize each finding into a ticket-ready specification.
2. Validate target `component` exists in known cells.
3. Preserve ordering constraints via `depends_on`.
4. Emit only actionable, scoped tickets.

## Guardrails

- One ticket spec per independently trackable bug.
- Do not include fixes for the current cell in this artifact.

