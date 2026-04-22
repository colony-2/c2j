# Multi-Skill Stepwise Example (New Process Per Step)

## Intent

Show how a recipe uses multiple skills with one `codex.exec` call per step, checkpointing between calls.

## Skill sequence (recommended)

1. `c2-ms-01-plan-step@v1`
2. `c2-ms-02-implement-step@v1`
3. `c2-ms-03-validate-step@v1`

## Process model

- Every step starts a new Codex process.
- Recipe passes prior `sessionId` to continue context.
- Each skill writes status to `implementation/latest-status.json`.
- Recipe decides whether to continue, revise, or route to user input.

## Example recipe shape (conceptual)

```yaml
states:
  codex_step:
    op: codex.exec
    inputs:
      sessionId: "${{ 'codex_step' in states ? states.codex_step.outputs.sessionId : '' }}"
      prompt: "${{ 'codex_step' in states ? 'continue' : 'start' }}"
      skills:
        - c2-ms-01-plan-step@v1
        - c2-ms-02-implement-step@v1
        - c2-ms-03-validate-step@v1
      step_control:
        mode: single_step
        max_skill_segments: 1
    transitions:
      - to: pre_implementation_review
        when: "artifact_json('implementation/latest-status.json').status == 'checkpoint_ready'"
      - to: user_input
        when: "artifact_json('implementation/latest-status.json').status == 'needs_user_input'"
      - to: ready_to_merge_review
        when: "artifact_json('validation/latest-status.json').status == 'ready_for_merge'"
```

## Status contract used by all three skills

```json
{
  "status": "checkpoint_ready | needs_user_input | ready_for_validation | ready_for_merge | blocked",
  "completed_skill": "skill-name@v1",
  "next_skill_candidates": ["optional-skill@v1"],
  "summary": "short text"
}
```
