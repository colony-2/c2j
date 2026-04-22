# c2-user-clarification-manager@v1

## Purpose

Translate implementation ambiguities into concise, structured prompts for the recipe input op.

## Required inputs (`/src/inbox`)

- `implementation/questions-raw.json` or unresolved ambiguity notes

## Required outputs (`/src/outbox/implementation`)

- `questions.json`: structured form model for user input.
- `question-context.md`: short reviewer context.

## `questions.json` shape

- `questions`: list of objects with:
  - `id`
  - `prompt`
  - `type` (`single_select` preferred)
  - `options` (2–4 explicit choices)
  - `recommended_option`
  - `impact`

## Execution steps

1. Minimize question count; batch only truly blocking decisions.
2. Prefer finite options over free text.
3. Keep prompts tied to concrete implementation decisions.
4. Write neutral recommendation rationale in `question-context.md`.

## Guardrails

- No open-ended “type anything” unless unavoidable.
- No more than one question batch per loop unless new blockers appear.

