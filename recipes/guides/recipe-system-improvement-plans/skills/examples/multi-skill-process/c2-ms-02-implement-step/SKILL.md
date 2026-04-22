# c2-ms-02-implement-step@v1

## Purpose

Apply planned code changes in the current cell and checkpoint with structured status.

## Inputs (`/src/inbox`)

- `implementation/plan.json`
- `.c2/tests/*.md`
- optional prior clarification answers

## Outputs (`/src/outbox/implementation`)

- `progress.ndjson`
- `summary.md`
- `latest-status.json`
- optional `questions.json`

## Required status behaviors

### If clarification is required

```json
{
  "status": "needs_user_input",
  "completed_skill": "c2-ms-02-implement-step@v1",
  "next_skill_candidates": ["c2-ms-02-implement-step@v1"],
  "summary": "Blocking ambiguity detected; user clarification required."
}
```

### If implementation is ready

```json
{
  "status": "checkpoint_ready",
  "completed_skill": "c2-ms-02-implement-step@v1",
  "next_skill_candidates": ["c2-ms-03-validate-step@v1"],
  "summary": "Implementation changes complete for validation."
}
```

## Rules

1. Never edit `.c2/tests/*.md` in this step.
2. Never use assistant-summary as downstream data source.
3. Return after one checkpointable segment.

