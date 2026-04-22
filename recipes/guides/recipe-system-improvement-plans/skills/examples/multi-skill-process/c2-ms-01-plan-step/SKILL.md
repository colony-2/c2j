# c2-ms-01-plan-step@v1

## Purpose

Create a concrete implementation plan artifact and return control at checkpoint.

## Inputs (`/src/inbox`)

- `requirements/plan.json`
- `requirements/index.md`

## Outputs (`/src/outbox/implementation`)

- `plan.json`
- `index.md`
- `latest-status.json`

## Required `latest-status.json`

```json
{
  "status": "checkpoint_ready",
  "completed_skill": "c2-ms-01-plan-step@v1",
  "next_skill_candidates": ["c2-ms-02-implement-step@v1"],
  "summary": "Implementation plan drafted and ready for next step."
}
```

## Rules

1. Do not implement code in this step.
2. Keep compatibility constraints explicit in plan artifacts.
3. Return immediately after writing artifacts.

