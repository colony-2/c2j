# c2-requirements-contrarian-review@v1

## Purpose

Challenge requirements quality, missing risks, and compatibility gaps before planning.

## Required inputs (`/src/inbox`)

- `requirements/plan.json`
- `requirements/index.md`
- `requirements/requirements/*` (optional)

## Required outputs (`/src/outbox/requirements`)

- `api-review.json`:
  - `ok` (bool)
  - `blocking_issues` (list)
  - `feedback` (list)

## Execution steps

1. Evaluate whether requirements are specific and testable.
2. Check compatibility constraints are explicit and sufficient.
3. Flag missing edge cases and dependency implications.
4. Return structured `ok` decision and clear blockers.

## Guardrails

- Prefer concise blocker statements with direct remediation guidance.
- Do not propose breaking behavior.

