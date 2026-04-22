# Contract

## Required artifacts

Write these files under `/src/outbox/requirements`:

- `api-review.json`
- `api-review.md`

## Required response shape

```json
{
  "ok": false,
  "feedback": "Concise reviewer guidance",
  "blocking_issues": [
    "Issue 1"
  ]
}
```

Rules:

- `ok` is `false` when `blocking_issues` is non-empty.
- `blocking_issues` should identify the exact compatibility, scope, or quality defect.
- `feedback` should summarize the repair direction.
