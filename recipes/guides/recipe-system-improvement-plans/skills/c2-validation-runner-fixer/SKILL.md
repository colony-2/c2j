# c2-validation-runner-fixer@v1

## Purpose

Run planned validation commands, capture outputs as artifacts, and apply in-scope fixes when safe.

## Required inputs (`/src/inbox`)

- `outcome/validation-commands.txt`
- current repo state

## Required outputs (`/src/outbox/validation`)

- `output.txt`
- `output-tail.txt`
- `latest-status.json`
- `summary.md`

## `latest-status.json` contract

- `status`: `passed | failed | blocked`
- `failed_commands`: list (optional)
- `next_action`: short guidance

## Execution steps

1. Execute commands in order and capture full output.
2. Record concise tail for UI display.
3. Apply safe in-scope fixes if failures are straightforward.
4. Re-run affected commands and write final status.

## Guardrails

- Keep all logs/artifacts in `/src/outbox/validation`.
- Do not rely on assistant summary parsing for downstream decisions.
- Do not change test statements at this stage.

