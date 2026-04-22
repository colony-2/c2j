# c2-ms-03-validate-step@v1

## Purpose

Run validation commands and produce merge-readiness status.

## Inputs (`/src/inbox`)

- `outcome/validation-commands.txt`
- repository working tree

## Outputs (`/src/outbox/validation`)

- `output.txt`
- `output-tail.txt`
- `latest-status.json`

## Required `validation/latest-status.json`

### Pass case

```json
{
  "status": "ready_for_merge",
  "completed_skill": "c2-ms-03-validate-step@v1",
  "next_skill_candidates": [],
  "summary": "Validation command plan passed."
}
```

### Failure/blocker case

```json
{
  "status": "blocked",
  "completed_skill": "c2-ms-03-validate-step@v1",
  "next_skill_candidates": ["c2-ms-02-implement-step@v1"],
  "summary": "Validation failed; implementation revision required."
}
```

## Rules

1. Persist logs as artifacts for UI visibility.
2. Keep output status deterministic for recipe routing.
3. Return after validation segment completes.
