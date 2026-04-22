# c2-implementation-plan-author@v1

## Purpose

Translate approved requirements into an execution plan with ordered dependency ticket specs when other cells must change first.

## Required inputs (`/src/inbox`)

- `requirements/plan.json`
- `requirements/index.md`
- `requirements/api-review.json` (optional)

## Required outputs (`/src/outbox/implementation`)

- `index.md`: human-reviewable implementation plan.
- `plan.json`:
  - `workstreams` (ordered list)
  - `backward_compatibility_checks` (list)
  - `requires_dependency_tickets` (bool)
  - `dependency_strategy` (string)
- `dependency-ticket-specs.json` (only if dependencies exist):
  - each item: `component`, `title`, `requestedChanges`, `depends_on`.

## Execution steps

1. Break work into coherent implementation workstreams.
2. Detect any prerequisite changes in other cells.
3. If prerequisites exist, emit dependency tickets with explicit dependency tree.
4. Keep plan artifact-focused; no direct ticket creation here.

## Guardrails

- Never suggest a plan that requires breaking changes.
- If dependency order matters, encode it via `depends_on`.

