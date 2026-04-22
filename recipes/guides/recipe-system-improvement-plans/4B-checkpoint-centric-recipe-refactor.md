# Plan 4B: Checkpoint-Centric Recipe Refactor (Skills Own Inner Loops)

## Goal

Refactor ticket recipes to preserve human checkpoints while delegating implementation loop internals to skills.

## Target shape for `new-ticket`

Keep explicit orchestration states:

1. `triage`
2. `requirements_planning`
3. `implementation_planning`
4. `outcome_determination`
5. `pre_implementation_review` (human checkpoint)
6. `implementation_supervisor` (`codex.exec` + skills)
7. `ready_to_merge_review` (human checkpoint)
8. `merge` / `cancel` / `done`

Collapse/remove explicit inner-loop states:

- `implement_resume`
- `pre_implementation_followup_review`
- `implement_bugs`
- `implement_resume_bugs`

## How backtracking still works

Backtracking remains explicit only at checkpoints:

- Pre-implementation checkpoint can route back to requirements/implementation-plan/outcome.
- Ready-to-merge checkpoint can route back to implementation or earlier phases.

Inner skill loops do not bypass checkpoint decisions.

## `implementation_supervisor` contract

`codex.exec` produces `implementation/latest-status.json`:

```json
{
  "status": "needs_user_input | needs_test_statement_update | has_dependency_tickets | ready_for_validation | blocked",
  "summary": "human readable summary",
  "questions": [],
  "dependency_ticket_specs": [],
  "requested_test_statement_changes": ""
}
```

Recipe uses one small router decision on `status`:

- `needs_user_input` -> back to `pre_implementation_review` with context from artifacts.
- `needs_test_statement_update` -> route to `outcome_determination`.
- `has_dependency_tickets` -> single `ticket.manage` call with provided specs.
- `ready_for_validation` -> `validate`.

## Progress reporting model

Checkpoint progress is visible via:

- checkpoint input prompts,
- `implementation/progress.ndjson`,
- `implementation/latest-status.json`,
- `ready_to_merge_review` summary context.

## Migration steps

1. Add skill-aware status artifact contract (4A prerequisite).
2. Refactor `new-ticket` states to checkpoint-centric structure.
3. Update tests to assert checkpoint transitions and status artifact mapping.
4. Publish and monitor story readability.

## Risks and mitigations

- Risk: less explicit branch coverage in recipe YAML.
  - Mitigation: stricter skill contract schemas + test fixtures for status variants.
- Risk: skill behavior drift.
  - Mitigation: version pinning per recipe (`skill@vX`) and governance plan (4C).

## Success criteria

1. Checkpoints remain first-class and auditable.
2. Inner-loop state count in recipe drops substantially.
3. Human reviewer sees equal or better progress clarity at checkpoints.
