# Contract

## Required artifacts

Write these files under `/src/outbox/triage`:

- `latest-status.json`
- `decision.md`

## Required JSON response shape

```json
{
  "cell_is_appropriate": true,
  "recommended_cell": "",
  "rationale": "Concise ownership rationale"
}
```

Rules:

- `cell_is_appropriate` is `true` only when the current cell is the best available fit.
- `recommended_cell` must be empty when `cell_is_appropriate=true`.
- `recommended_cell` must exactly match one provided cell name when `cell_is_appropriate=false`.
- `rationale` must be concise and specific.
