# c2-triage-cell-boundary@v1

## Purpose

Determine whether the ticket belongs to the current cell and produce a reassignment recommendation when needed.

## Required inputs (`/src/inbox`)

- `context/ticket.json`: ticket id/title/description/current cell.
- `context/cells.json`: valid project cells.

## Required outputs (`/src/outbox/triage`)

- `decision.md`: plain-language rationale.
- `latest-status.json`:
  - `cell_is_appropriate` (bool)
  - `recommended_cell` (string, empty if in-cell)
  - `recommended_cell_is_valid` (bool)
  - `rationale` (string)

## Execution steps

1. Read ticket intent and compare to cell mandate.
2. If out-of-cell, choose exactly one cell from `context/cells.json`.
3. Write deterministic status JSON and human-readable rationale.
4. Do not create tickets directly in this skill.

## Guardrails

- Never invent a cell name not present in the provided list.
- If uncertain, set `cell_is_appropriate=true` and explain ambiguity.

