# c2-outcome-contrarian-review@v1

## Purpose

Validate that test statements and validation commands are outcome-focused, complete, and compatible.

## Required inputs (`/src/inbox`)

- `outcome/plan.json`
- `outcome/tests-index.md`
- `outcome/validation-commands.txt`
- `.c2/tests/*.md` (current repo state)

## Required outputs (`/src/outbox/outcome`)

- `review.json`:
  - `ok` (bool)
  - `blocking_issues` (list)
  - `feedback` (list)

## Execution steps

1. Check statement quality and completeness against requirements.
2. Confirm critical behavior has both positive and negative coverage.
3. Validate command plan is executable and scoped.
4. Emit `ok=false` with blockers when gaps are material.

## Guardrails

- Reject statements that are implementation-detail heavy.
- Reject omissions that would hide compatibility regressions.

