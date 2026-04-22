# Plan 4E: `codex.exec` User API Changes + Multi-Skill Stepwise Examples

## Goal

Define a concrete user-facing API for stepwise skill execution with checkpoint returns, using a **new Codex process per call** and optional `sessionId` resume.

## Design constraints

1. No long-lived Codex worker process.
2. Recipe remains checkpoint owner.
3. Skills exchange state through artifacts.
4. One checkpointable segment per `codex.exec` call.

## Proposed `codex.exec` user API (vNext)

## Input additions

```json
{
  "prompt": "continue",
  "sessionId": "optional-existing-session",
  "skills": [
    "c2-implementation-plan-author@v1",
    "c2-implementation-loop@v1",
    "c2-validation-runner-fixer@v1"
  ],
  "skill_mode": "enforce",
  "step_control": {
    "mode": "single_step",
    "max_skill_segments": 1,
    "return_on": [
      "checkpoint_ready",
      "needs_user_input",
      "needs_dependency_tickets",
      "needs_test_statement_update",
      "ready_for_validation",
      "ready_for_merge",
      "blocked"
    ]
  },
  "status_contract": {
    "path": "implementation/latest-status.json"
  },
  "worktree_path": "/path/to/repo",
  "cell_relative_path": "cells/example"
}
```

### Input field definitions
[4E-codex-exec-user-api-and-multi-skill-examples.md](4E-codex-exec-user-api-and-multi-skill-examples.md)
| Field | Type | Required | Behavior |
|---|---|---|---|
| `skills` | `string[]` | No | Declares allowed skill set for this call. |
| `skill_mode` | `string` | No | `enforce \| prefer \| off`; defaults to `off`. |
| `step_control.mode` | `string` | No | `single_step` forces one checkpointable segment per call. |
| `step_control.max_skill_segments` | `number` | No | Hard limit for segments; `1` is required for checkpoint-first recipes. |
| `step_control.return_on` | `string[]` | No | Status values that immediately return control to recipe. |
| `status_contract.path` | `string` | No | Artifact path containing authoritative status JSON. |

## Output additions

```json
{
  "status": "completed",
  "sessionId": "session-id",
  "step": {
    "completed_skill": "c2-implementation-loop@v1",
    "segment_count": 1,
    "checkpoint_status": "needs_user_input",
    "next_skill_candidates": [
      "c2-user-clarification-manager@v1"
    ],
    "status_artifact": "implementation/latest-status.json"
  },
  "assistantSummary": "optional human summary"
}
```

### Output field definitions

| Field | Type | Required | Behavior |
|---|---|---|---|
| `step.completed_skill` | `string` | Yes | Skill completed in this process invocation. |
| `step.segment_count` | `number` | Yes | Number of executed segments; must respect `max_skill_segments`. |
| `step.checkpoint_status` | `string` | Yes | Mirrors status from `status_contract.path`. |
| `step.next_skill_candidates` | `string[]` | No | Candidate skills for next call; recipe may ignore. |
| `step.status_artifact` | `string` | Yes | Artifact reference to authoritative status payload. |

## Compatibility rules

1. Existing `prompt` + `sessionId` behavior remains valid.
2. Existing recipes with no `skills` continue unchanged.
3. `assistantSummary` remains optional and non-authoritative.
4. Artifact-backed status becomes authoritative for routing.

## Recipe usage pattern (checkpoint-first)

1. Call `codex.exec` with `step_control.mode=single_step`.
2. Read `implementation/latest-status.json`.
3. If status is checkpoint-worthy, route to recipe checkpoint/input state.
4. On `continue`, invoke `codex.exec` again with prior `sessionId` and same skill set.

This preserves per-step process isolation while retaining conversation continuity.

## Multi-skill example set

Concrete example skills are provided in:

- `guides/recipe-system-improvement-plans/skills/examples/multi-skill-process/README.md`
- `guides/recipe-system-improvement-plans/skills/examples/multi-skill-process/c2-ms-01-plan-step/SKILL.md`
- `guides/recipe-system-improvement-plans/skills/examples/multi-skill-process/c2-ms-02-implement-step/SKILL.md`
- `guides/recipe-system-improvement-plans/skills/examples/multi-skill-process/c2-ms-03-validate-step/SKILL.md`

These examples demonstrate how one recipe can call `codex.exec` repeatedly, each call in a new process, while progressing through multiple skills via `sessionId`.
