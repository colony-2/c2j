# Contract

## Required artifacts

Write these files under `/src/outbox/outcome`:

- `plan.json`
- `tests-index.md`
- `validation-commands.txt`
- `index.md`

Also update or create test statement files under `.c2/tests/*.md` in the repo worktree.

## Required `plan.json` shape

Top-level keys:

- `summary` string
- `current_test_statements_summary` string
- `test_statement_updates_required` boolean
- `test_statement_repo_glob` string
- `validation_commands` string
- `notes_for_user_review` string

Rules:

- `test_statement_repo_glob` should point at the authoritative statement location.
- `validation_commands` should be newline-separated runnable commands.
- Only `.c2/tests/*.md` repo files may be changed during this stage.
- Use markdown bullets in this canonical format:
  `- [files: ...] [importance: high|medium|low] [type: unit|integration] [deps: ...] [polarity: positive|negative] Statement text`
