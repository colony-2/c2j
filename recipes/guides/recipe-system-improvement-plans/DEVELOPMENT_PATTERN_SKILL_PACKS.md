# Skill Packs and Checkpoint-Oriented Recipe Usage

## Objective

Package concrete skills into stable bundles so recipes can stay checkpoint-centric and avoid state explosion.

Concrete skill definitions live under `guides/recipe-system-improvement-plans/skills/`.

## Pack definitions (v1)

## `c2-planning-pack@v1`

Includes:

- `c2-triage-cell-boundary@v1`
- `c2-requirements-author@v1`
- `c2-requirements-contrarian-review@v1`
- `c2-implementation-plan-author@v1`
- `c2-implementation-compat-review@v1`
- `c2-test-statement-curator@v1`
- `c2-outcome-contrarian-review@v1`

Use for:

- triage through outcome stages (pre-implementation checkpoint input remains recipe-managed).

## `c2-implementation-pack@v1`

Includes:

- `c2-implementation-loop@v1`
- `c2-cross-cell-bug-ticket-drafter@v1`
- `c2-user-clarification-manager@v1`
- `c2-validation-runner-fixer@v1`

Use for:

- implementation inner loop and validation-prep behavior.

## `c2-merge-pack@v1`

Includes:

- `c2-merge-readiness-summarizer@v1`
- `c2-completion-note-writer@v1`

Use for:

- ready-to-merge briefing and final completion note composition.

## Checkpoint-preserving recipe pattern

Keep explicit recipe checkpoints:

1. `pre_implementation_review` (human)
2. `ready_to_merge_review` (human)

Everything else can delegate to skill packs via `codex.exec`.

## Example recipe shape (conceptual)

```yaml
states:
  requirements_planning:
    op: codex.exec
    inputs:
      skills: [c2-planning-pack@v1]
  implementation_supervisor:
    op: codex.exec
    inputs:
      skills: [c2-implementation-pack@v1]
  ready_to_merge_review:
    op: input
  merge_readiness:
    op: codex.exec
    inputs:
      skills: [c2-merge-pack@v1]
```

## Required status contract for `implementation_supervisor`

Skill pack must produce `implementation/latest-status.json` with:

- `status`: `ready_for_validation | needs_user_input | needs_dependency_tickets | needs_test_statement_update | blocked`
- `summary`
- `questions` (optional)
- `dependency_ticket_specs` (optional)
- `requested_test_statement_changes` (optional)

Recipe routes only on `status`, not on internal skill details.

## How this preserves progress visibility

1. Checkpoints remain explicit states in the recipe graph.
2. Skill progress is surfaced as artifacts (`progress.ndjson`, `latest-status.json`).
3. Human reviewers consume consolidated summaries at checkpoints.

## Migration path for existing `new-ticket`

1. Keep current states and add skill-pack outputs in parallel.
2. Replace `implement` + `implement_resume` + follow-up states with one `implementation_supervisor`.
3. Keep both checkpoints unchanged.
4. Update tests to assert status-artifact routing, not internal state-by-state loops.

## Acceptance criteria

1. Same checkpoint decisions available as today.
2. Fewer orchestration states for implementation internals.
3. All downstream handoff uses artifacts, no assistant-summary parsing dependency.
