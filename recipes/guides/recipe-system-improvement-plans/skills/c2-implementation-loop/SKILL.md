# c2-implementation-loop@v1

## Purpose

Execute implementation iterations until ready for validation or a structured blocker outcome is reached.

## Required inputs (`/src/inbox`)

- `requirements/*`
- `implementation/plan.json`
- `outcome/validation-commands.txt`
- `.c2/tests/*.md`
- optional prior responses to implementation questions

## Required outputs (`/src/outbox/implementation`)

- `latest-status.json`
- `progress.ndjson`
- `summary.md`
- optional `questions.json`
- optional `dependency-ticket-specs.json`
- optional `test-statement-change-request.md`

## `latest-status.json` contract

- `status`: `ready_for_validation | needs_user_input | needs_dependency_tickets | needs_test_statement_update | blocked`
- `summary`: short description
- `details`: optional structured object

## Execution steps

1. Implement plan workstreams while preserving compatibility.
2. Run relevant checks from the validation command plan during iteration.
3. If blocked by unknown requirements, emit `questions.json` and set `needs_user_input`.
4. If blocked by cross-cell defects, emit `dependency-ticket-specs.json` and set `needs_dependency_tickets`.
5. If test statement updates are needed, emit `test-statement-change-request.md` and set `needs_test_statement_update`.
6. Otherwise set `ready_for_validation`.

## Guardrails

- Never edit `.c2/tests/*.md` in this stage.
- Never apply cross-cell direct fixes; route to tickets.
- Do not pass data through assistant summary when artifacts exist.

