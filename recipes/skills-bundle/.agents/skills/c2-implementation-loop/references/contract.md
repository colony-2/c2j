# Contract

## Required artifacts under `/src/outbox/implementation`

Always write:

- `latest-status.json`
- `progress.ndjson`
- `summary.md`

Optional when needed:

- `questions.json`
- `dependency-ticket-specs.json`
- `test-statement-change-request.md`

## Required `latest-status.json` shape

```json
{
  "status": "ready_for_validation",
  "summary": "Short status summary",
  "details": {}
}
```

Allowed `status` values:

- `ready_for_validation`
- `needs_user_input`
- `needs_dependency_tickets`
- `needs_test_statement_update`
- `blocked`

## Pending dependency contract

When the recipe prompt requests `pendingDependencies`, each entry should contain:

- `component`: exact target cell name
- `requestedChanges`: bug summary, impact, and concrete repro or fix guidance
