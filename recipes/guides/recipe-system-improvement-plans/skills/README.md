# C2 Development Pattern Skills (`v1` templates)

These are executable `SKILL.md` templates for the development pattern described in this repo.

## How to use

1. Pick the skill directory.
2. Run `codex.exec` with that skill enabled.
3. Pass required inputs as artifacts to the op.
4. Route recipe state transitions only from the skill status artifacts.

## Checkpoint model

- Human checkpoint 1: pre-implementation review.
- Human checkpoint 2: ready-to-merge review.
- Skills handle inner loops; recipes stay orchestration-focused.

## Skill set

- `c2-triage-cell-boundary`
- `c2-requirements-author`
- `c2-requirements-contrarian-review`
- `c2-implementation-plan-author`
- `c2-implementation-compat-review`
- `c2-test-statement-curator`
- `c2-outcome-contrarian-review`
- `c2-implementation-loop`
- `c2-cross-cell-bug-ticket-drafter`
- `c2-user-clarification-manager`
- `c2-validation-runner-fixer`
- `c2-merge-readiness-summarizer`
- `c2-completion-note-writer`

## Examples

- Multi-skill stepwise example (new process per step):
  - `guides/recipe-system-improvement-plans/skills/examples/multi-skill-process/README.md`

## Global contracts

- Inputs are read from `/src/inbox`.
- Outputs are written to `/src/outbox`.
- Downstream ops consume artifacts, not assistant-summary strings.
- Status routing files are JSON and must be deterministic.
- Backward compatibility is mandatory.
