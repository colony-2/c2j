# c2-test-statement-curator@v1

## Purpose

Define outcome test statements before implementation and produce validation command plans.

## Required inputs (`/src/inbox`)

- `requirements/plan.json`
- `implementation/plan.json`
- Existing `.c2/tests/*.md` from repo

## Required outputs (`/src/outbox/outcome`)

- `plan.json`
- `tests-index.md`
- `validation-commands.txt`
- `test-statement-delta.md`

## Repository write expectations

- Update `.c2/tests/*.md` in the target cell during this stage only.

## Test statement rules

Each statement must:

- be markdown and 30 words or less,
- include relevant filename(s),
- include relative importance,
- include `unit` or `integration` and dependencies,
- use business/expectation language,
- include positive and negative cases for critical functionality.

## Execution steps

1. Compare existing statements with planned behavior.
2. Add/update statements needed for the intended outcome.
3. Write an explicit validation command plan.
4. Summarize statement changes in `test-statement-delta.md`.

## Guardrails

- Implementation stage must not edit `.c2/tests/*.md`.
- If later changes are needed, request via artifact instead of editing directly.

