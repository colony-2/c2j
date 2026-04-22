---
name: c2-implementation-loop
description: Execute ticket implementation inside the current cell and return structured checkpoint status for recipe routing, blockers, and validation handoff.
metadata:
  short-description: Implement safely with checkpointed c2 status
---

# c2-implementation-loop

Use this skill when a recipe needs Codex to implement approved work, stop at meaningful checkpoints, and communicate blockers in machine-routable form.

## Workflow

1. Read the requirements, implementation, and outcome artifacts plus any prior validation feedback.
2. Make only current-cell code changes.
3. Decide whether to continue, request clarification, request test-statement updates, request dependency tickets, report blocked, or hand off to validation.
4. Write the required implementation artifacts and status file.

## Checkpoint Rules

- `needs_user_input`: use when ambiguity blocks safe progress; ask numbered questions.
- `needs_dependency_tickets`: use when another cell must change before safe completion; write dependency ticket specs.
- `needs_test_statement_update`: use when `.c2/tests/*.md` must change before implementation can proceed safely.
- `blocked`: use when neither local implementation nor a narrower checkpoint can safely proceed.
- `ready_for_validation`: use only when current-cell code and tests are updated, no critical blocker remains, and `.c2/tests/*.md` was left untouched.

## Guardrails

- Never edit `.c2/tests/*.md` during implementation.
- Never make direct cross-cell fixes.
- Never introduce backward-incompatible changes.
- Keep changes small, safe, and aligned with the approved artifacts.
- Use artifact files for any downstream-critical data.

## Quality Bar

- `summary` should state what changed or why progress is blocked.
- Questions and dependency tickets must be specific enough for immediate downstream action.
- Do not report `ready_for_validation` if validation is unlikely to pass.

Read `references/contract.md` for required artifacts, status values, and pending dependency fields.
