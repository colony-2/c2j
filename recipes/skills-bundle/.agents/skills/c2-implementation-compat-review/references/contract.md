# Contract

## Required artifacts

Write these files under `/src/outbox/implementation`:

- `compat-review.json`
- `compat-review.md`

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
- A blocking issue must identify the unsafe compatibility, sequencing, or ownership defect.
