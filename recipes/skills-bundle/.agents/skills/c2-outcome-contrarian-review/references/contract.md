# Contract

## Required artifacts

Write these files under `/src/outbox/outcome`:

- `review.json`
- `review.md`

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
- Each blocking issue should name the missing coverage, format defect, or compatibility problem.
- Canonical statement format to enforce:
  `- [files: ...] [importance: high|medium|low] [type: unit|integration] [deps: ...] [polarity: positive|negative] Statement text`
