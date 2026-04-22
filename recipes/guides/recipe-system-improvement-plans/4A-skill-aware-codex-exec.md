# Plan 4A: Skill-Aware `codex.exec` with Structured Progress Contracts

## Goal

Keep recipe checkpoints explicit while moving repetitive inner loops into Codex Skills.

For concrete user API fields and multi-skill examples, see:

- `4E-codex-exec-user-api-and-multi-skill-examples.md`

## Proposed system enhancements

## 1) Add skill inputs to `codex.exec`

```yaml
inputs:
  skills:
    - c2-implementation-loop@v1
    - c2-cross-cell-bug-reporter@v1
    - c2-test-statement-gap-detector@v1
  skill_mode: enforce   # enforce | prefer | off
  skill_config_artifact: implementation/skill-config.json
```

- `enforce`: fail if required skill unavailable.
- `prefer`: best effort.

## 2) Add standard progress/event outputs for long-running skill loops

`codex.exec` output contract (new fields):

- `progress_state`: `running | waiting_user | waiting_dependencies | ready_for_merge | failed`
- `next_action`: short machine-readable action code.
- `event_artifact`: artifact key of latest event payload.

Required outbox artifacts (written by skills, not recipe ops):

- `implementation/progress.ndjson`
- `implementation/latest-status.json`
- `implementation/questions.json` (if user input needed)
- `implementation/dependency-ticket-specs.json` (if cross-cell bugs found)
- `implementation/test-statement-change-request.md` (if statement changes requested)

## 3) Add built-in artifact-to-output mapping helpers

Recipe author can map artifacts to outputs without shell/jq conversion:

```yaml
outputs:
  latest_status: '${{ artifact_json("implementation/latest-status.json") }}'
```

## Recipe impact

Recipes keep explicit checkpoints, but remove detailed wiring for:

- repeated resume/pause transitions,
- duplicate question/retry gates,
- custom parsing glue for status handoff.

## Migration approach

1. Add `skills` support to `codex.exec`.
2. Publish initial skill pack and artifact contract.
3. Update one implementation state in `new-ticket` to use skill outputs.
4. Keep old path behind feature flag until parity is proven.

## Compatibility

- Backward compatible: `skills` is optional.
- Existing recipes continue unchanged.

## Success criteria

1. `new-ticket` implementation orchestration state count drops while behavior parity remains.
2. Checkpoint visibility remains explicit in recipe graph.
3. No regressions in current recipe tests.
