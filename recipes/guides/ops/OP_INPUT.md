# input Op

Collects user input either as a single question or as a multi-field form; returns the response and user metadata.

## What you can ask for

- **Single question mode:** Provide `question` and a `type` (no `fields`). Good for quick prompts like short text, date, or a single choice.
- **Multi-field form mode:** Omit `question` and supply an array of `fields` (each with its own `type`). Optional `title` adds a heading.
- **Context:** Attach artifacts for the UI to show alongside the form.
- **Autofill:** Auto fill the response so the op can complete without user action. Typically used for testing or replay.

## Input schema (top level)

```json
{
  "form": {
    "question": "What should the title be?",
    "type": "short_answer",
    "title": "Optional form title",
    "fields": [],
    "options": [],
    "scale": null,
    "context": {
      "artifacts": [],
      "artifacts_from_output": "",
      "artifacts_glob": []
    },
    "autofill": {
      "response": "pre-filled answer",
      "fields": {},
      "user_id": "user-123",
      "metadata": {
        "source": "ui"
      }
    }
  }
}
```

Only one of `question` or `fields` is needed; set `fields` for multi-field forms.

## Field definitions

Each entry in `fields` uses this shape:

```json
{
  "id": "priority",
  "type": "dropdown",
  "question": "Priority",
  "required": true,
  "placeholder": "Pick one",
  "options": [
    { "value": "low", "label": "Low" },
    { "value": "high", "label": "High" }
  ],
  "scale": {
    "min": 1,
    "max": 5,
    "min_label": "Low",
    "max_label": "High"
  },
  "validation": {
    "min_length": 0,
    "max_length": 120,
    "pattern": "",
    "min": 0,
    "max": 10
  }
}
```

### Supported `type` values

- `short_answer` (single-line text)
- `paragraph_text` (multi-line text)
- `multiple_choice` (exactly one option; `options` required)
- `checkboxes` (one or many options; `options` required)
- `dropdown` (`options` required)
- `linear_scale` (numeric scale; `scale.min` 0-10, `scale.max` 1-10, optional labels)
- `multiple_choice_grid` (choose one per row/column set; use `options` to define choices)
- `checkbox_grid` (multiple selections across grid; use `options`)
- `date`
- `time`
- `file_upload`

`linear_scale`, grid types, and choice types ignore `placeholder`. `options` entries use `{ "value": "...", "label": "..." }` (`label` optional but recommended).

### Validation helpers

- `min_length` / `max_length` apply to text fields.
- `pattern` is a regex string for text inputs.
- `min` / `max` apply to numeric scale-style inputs.
- `required` enforces a non-empty answer for that field.

## Context payload shown with the form

```json
{
  "artifacts": [{ "path": "docs/ADR-001.md" }],
  "artifacts_from_output": "previous_step.outputs.report",
  "artifacts_glob": [{ "pattern": "reports/*.md" }]
}
```

## Autofill behavior

Set `form.autofill` to an `Output` payload. If any field in `autofill` is non-zero (response, fields, user_id, or metadata), the op schedules an internal `auto-fill-input` task and immediately returns that payload without user interaction.

Example:

```json
{
  "form": {
    "question": "What should the title be?",
    "type": "short_answer",
    "autofill": {
      "response": "Ship it!",
      "user_id": "system",
      "metadata": { "source": "recipe-default" }
    }
  }
}
```

## Output Structure

```json
{
  "response": "single answer for question mode",
  "fields": {
    "priority": "high",
    "tags": ["backend", "ux"]
  },
  "user_id": "user-123",
  "metadata": {
    "source": "ui"
  }
}
```

`response` is populated for single-question mode; `fields` is used for multi-field forms. Only one is typically present. `user_id` defaults to `"anonymous"` when not provided in the submission.
