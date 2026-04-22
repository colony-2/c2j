# c2-requirements-author@v1

## Purpose

Produce a requirements bundle that defines intended behavior, constraints, compatibility expectations, and dependency candidates.

## Required inputs (`/src/inbox`)

- `ticket/ticket.json`
- `triage/latest-status.json` (optional)
- Prior reviewer feedback artifacts (optional)

## Required outputs (`/src/outbox/requirements`)

- `index.md`: requirements summary for human review.
- `plan.json`:
  - `goals` (list)
  - `non_goals` (list)
  - `constraints` (list)
  - `compatibility_requirements` (list)
  - `candidate_dependencies` (list of cell-level items)
- `requirements/`: detailed markdown files as needed.

## Execution steps

1. Convert ticket intent into concrete, testable requirement language.
2. Explicitly state backward-compatibility expectations.
3. Identify potential dependency cells without creating tickets.
4. Write artifacts only; no user prompt in this skill.

## Guardrails

- No implementation details unless needed for requirement clarity.
- No backward-incompatible requirement is allowed.

