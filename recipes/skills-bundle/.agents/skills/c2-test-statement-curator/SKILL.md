---
name: c2-test-statement-curator
description: Define or update outcome test statements and validation commands before implementation, keeping them outcome-focused and compatibility-safe.
metadata:
  short-description: Author c2 test statements and validation plans
---

# c2-test-statement-curator

Use this skill when a ticket needs outcome determination before implementation begins.

## Workflow

1. Read the requirements and implementation plan artifacts.
2. Inspect existing `.c2/tests/*.md` statements relevant to the ticket.
3. Decide whether statements need updates.
4. Update only `.c2/tests/*.md`, write the required outcome artifacts, and return only the exact `plan.json` object.

## Authoring Rules

- Keep statements in markdown and focused on observable behavior.
- Every statement line must stay within 30 words.
- Each statement must include filenames, importance, test type plus dependencies, and polarity.
- Use one markdown bullet per statement with this annotation pattern before the statement text:
  `- [files: ...] [importance: high|medium|low] [type: unit|integration] [deps: ...] [polarity: positive|negative] Statement text`
- Critical behavior needs both positive and negative coverage.
- Prefer integration points and boundary behavior over trivial internals.
- Validation commands must be enough to check the changed surface in this cell.
- Do not edit application source code in this stage.

## Quality Bar

- The statement set is clearly tied to the ticket scope.
- Validation commands match the ecosystem in the cell repo.
- `test_statement_updates_required` reflects reality, not optimism.
- `tests-index.md` makes the authoritative statement files easy to review.

Read `references/contract.md` for the exact artifact and schema requirements.
