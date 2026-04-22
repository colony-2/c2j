# c2-completion-note-writer@v1

## Purpose

Write the final completion note and structured completion payload once merge is done.

## Required inputs (`/src/inbox`)

- `merge/readiness-summary.md`
- merge op result artifacts
- implementation/validation summary artifacts

## Required outputs (`/src/outbox/completion`)

- `note.md`
- `summary.json`

## `summary.json` shape

- `ticket_outcome` (`done|cancelled`)
- `merged_hash` (string, if done)
- `highlights` (list)
- `follow_ups` (list)

## Execution steps

1. Produce concise, human-readable completion narrative.
2. Include verification outcomes and compatibility statement.
3. Capture follow-up items explicitly.

## Guardrails

- For successful completion, recipe should transition ticket to `done`.
- Avoid code-level detail unless required to explain user-visible impact.

