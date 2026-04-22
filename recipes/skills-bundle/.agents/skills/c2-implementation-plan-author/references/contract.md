# Contract

## Required artifacts

Write these files under `/src/outbox/implementation`:

- `plan.json`
- `index.md`
- one markdown file per dependency ticket spec: `<REQ-ID>.md`

## Required `plan.json` shape

Top-level keys:

- `summary` string
- `requires_dependency_tickets` boolean
- `dependency_order` array of requirement IDs
- `dependency_ticket_specs` array
- `local_steps` array of strings
- `notes_for_user_review` string

Each dependency ticket spec must include:

- `id`
- `title`
- `target_cell`
- `depends_on_ids`
- `depends_on_markdown`
- `scope`
- `acceptance_criteria_markdown`
- `risks_markdown`
- `notes`

Rules:

- `dependency_order` must remain topologically valid.
- `dependency_ticket_specs` must target only non-current cells.
- `depends_on_ids` may reference only requirement IDs present in the approved plan.
