# Concrete Skill Catalog for the C2 Development Pattern

## Intent

These are the concrete skills to operationalize your lifecycle:

1. triage
2. requirements
3. implementation planning
4. outcome/test statements
5. implementation
6. validation
7. merge/completion

The recipe remains responsible for stage checkpoints and ticket state transitions. Skills handle inner-loop work.

Executable `SKILL.md` templates for this catalog are available in:

- `guides/recipe-system-improvement-plans/skills/`

## Common skill contract

Each skill should follow the same contract shape:

- **Reads**: `/src/inbox/*` artifacts + repo working tree.
- **Writes**: structured artifacts to `/src/outbox/<phase>/...`.
- **Status file**: `/src/outbox/<phase>/latest-status.json`.
- **Progress log**: `/src/outbox/<phase>/progress.ndjson`.
- **No silent side channels**: if data matters downstream, write an artifact.

## Skill list (recommended v1 set)

## 1) `c2-triage-cell-boundary@v1`

- **Stage**: triage
- **Responsibility**: decide whether ticket belongs to current cell; recommend valid target cell when out-of-scope.
- **Inputs**: ticket title/description, `cells()` list.
- **Outputs**:
  - `triage/latest-status.json` (`cell_is_appropriate`, `recommended_cell`, `rationale`)
  - `triage/decision.md`

## 2) `c2-requirements-author@v1`

- **Stage**: requirements
- **Responsibility**: produce requirements bundle and dependency ordering.
- **Inputs**: ticket + prior feedback.
- **Outputs**:
  - `requirements/plan.json`
  - `requirements/index.md`
  - `requirements/requirements/*.md`

## 3) `c2-requirements-contrarian-review@v1`

- **Stage**: requirements review
- **Responsibility**: challenge backward-compatibility and API-risk gaps.
- **Inputs**: `requirements/plan.json`, `requirements/index.md`.
- **Outputs**:
  - `requirements/api-review.json` (`ok`, `blocking_issues`, `feedback`)

## 4) `c2-implementation-plan-author@v1`

- **Stage**: implementation planning
- **Responsibility**: convert requirements into execution plan + dependency ticket specs.
- **Inputs**: requirements artifacts.
- **Outputs**:
  - `implementation/plan.json`
  - `implementation/index.md`
  - `implementation/dependency-ticket-specs.json`

## 5) `c2-implementation-compat-review@v1`

- **Stage**: implementation planning review
- **Responsibility**: detect backward-incompatible rollout risk.
- **Inputs**: implementation plan artifacts.
- **Outputs**:
  - `implementation/compat-review.json` (`ok`, `blocking_issues`, `feedback`)

## 6) `c2-test-statement-curator@v1`

- **Stage**: outcome determination
- **Responsibility**: define/adjust `.c2/tests/*.md` statement changes and validation command plan.
- **Inputs**: requirements + implementation plan + existing `.c2/tests/*.md`.
- **Outputs**:
  - `outcome/plan.json`
  - `outcome/tests-index.md`
  - `outcome/validation-commands.txt`
  - `outcome/test-statement-delta.md`

## 7) `c2-outcome-contrarian-review@v1`

- **Stage**: outcome review
- **Responsibility**: verify statements are business-facing, bounded, and compatible.
- **Inputs**: outcome artifacts.
- **Outputs**:
  - `outcome/review.json` (`ok`, `blocking_issues`, `feedback`)

## 8) `c2-implementation-loop@v1`

- **Stage**: implementation (inner loop)
- **Responsibility**: execute code/test changes until one of terminal statuses is reached.
- **Inputs**: requirements/implementation/outcome artifacts + repo + `.c2/tests/*.md`.
- **Outputs**:
  - `implementation/latest-status.json`
  - `implementation/progress.ndjson`
  - `implementation/summary.md`
  - optional:
    - `implementation/questions.json`
    - `implementation/dependency-ticket-specs.json`
    - `implementation/test-statement-change-request.md`

`latest-status.json` should use:

- `ready_for_validation`
- `needs_user_input`
- `needs_dependency_tickets`
- `needs_test_statement_update`
- `blocked`

## 9) `c2-cross-cell-bug-ticket-drafter@v1`

- **Stage**: implementation support
- **Responsibility**: normalize discovered cross-cell bugs into ticket-ready specs.
- **Inputs**: implementation findings.
- **Outputs**:
  - `implementation/dependency-ticket-specs.json`
  - each entry includes `component`, `title`, `requestedChanges`, `depends_on`.

## 10) `c2-user-clarification-manager@v1`

- **Stage**: implementation support
- **Responsibility**: convert missing assumptions into concise, structured user questions.
- **Inputs**: implementation blockers.
- **Outputs**:
  - `implementation/questions.json`
  - `implementation/question-context.md`

## 11) `c2-validation-runner-fixer@v1`

- **Stage**: post-implementation
- **Responsibility**: execute validation command plan, interpret failures, apply safe fixes when in scope.
- **Inputs**: `outcome/validation-commands.txt` + repo.
- **Outputs**:
  - `validation/output.txt`
  - `validation/output-tail.txt`
  - `validation/latest-status.json` (`passed`, `failed`, `blocked`)

## 12) `c2-merge-readiness-summarizer@v1`

- **Stage**: ready-to-merge checkpoint support
- **Responsibility**: summarize what changed, test outcomes, and open risks for human merge decision.
- **Inputs**: validation + implementation artifacts + git metadata.
- **Outputs**:
  - `merge/readiness-summary.md`
  - `merge/risk-checklist.json`

## 13) `c2-completion-note-writer@v1`

- **Stage**: completion
- **Responsibility**: generate final human-readable ticket completion note from artifacts.
- **Inputs**: all prior stage artifacts + merge output.
- **Outputs**:
  - `completion/note.md`
  - `completion/summary.json`

## Non-negotiable guardrails for all skills

1. Never introduce backward-incompatible behavior.
2. Never modify `.c2/tests/*.md` during implementation loop; request changes via artifact.
3. Always prefer artifact handoff over assistant-summary parsing.
4. Keep cross-cell fixes as tickets, not direct edits.
5. Emit structured status so recipe orchestration remains simple and deterministic.
