# Contract

## Required artifacts

Write these files under `/src/outbox/requirements`:

- `plan.json`
- `index.md`
- one markdown file per requirement: `<REQ-ID>.md`

## Required `plan.json` shape

Top-level keys:

- `summary` string
- `needs_cross_cell_support` boolean
- `dependency_order` array of requirement IDs
- `requirements` array
- `notes_for_user_review` string

Each requirement object must include:

- `id`
- `title`
- `target_cell`
- `depends_on`
- `scope`
- `api_changes`
- `acceptance_criteria`
- `risks`
- `open_questions`

Each `api_changes` entry must include:

- `service`
- `change_type`
- `description`
- `backwards_compatible`
- `migration_plan`

Rules:

- `dependency_order` must be a valid topological order of all requirement IDs.
- `depends_on` may reference only requirement IDs present in the plan.
- `target_cell` must be one provided cell name.
- `backwards_compatible` must be `true` for every proposed API change.
