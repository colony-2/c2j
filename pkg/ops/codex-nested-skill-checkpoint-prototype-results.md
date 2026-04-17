# Nested Skill Checkpoint Prototype Results

## Objective

Validate whether nested skills can be represented as checkpoint-return conditions in a single `codex.exec` invocation and resumed in a new process.

## Prototype Setup

1. Created local skills under a workspace-scoped `.codex/skills/` directory:
- `parent-checkpoint`
- `child-checkpoint`
2. `parent-checkpoint` required invoking `child-checkpoint` and writing:
- `outbox/implementation/latest-status.json` with nested checkpoint metadata
3. `child-checkpoint` wrote:
- `outbox/child/latest-status.json` with `status=needs_user_input`
4. Ran direct Codex CLI with JSON output and structured schema.

## What Worked

1. Codex loaded and followed local skills from workspace `.codex/skills`.
2. Nested behavior was executable in one invocation:
- child status artifact was created
- parent nested status artifact was created
3. Artifacts were deterministic and machine-readable for routing.

## Key Findings

1. `SKILL.md` requires YAML frontmatter (`---` block with `name` and `description`).
2. Overriding `CODEX_HOME` without credentials caused auth failures; workspace `.codex/skills` avoided this.
3. JSONL stream did not provide reliable dedicated "skill boundary" events for routing.
4. Therefore, checkpoint orchestration must rely on artifact contracts, not event stream parsing.

## Recommended Solution for Nested Checkpoints

1. Treat nested checkpoints exactly like top-level checkpoints in op return logic.
2. Include checkpoint `scope` and `stack` in output:
- `scope: top_level | nested`
- ordered `stack` of active skills
3. Require `status_artifact` for the blocking nested skill.
4. Return immediately when nested checkpoint conditions are present.
5. Preserve stack in `resume_context` so next invocation can resume correctly in a fresh process.

## Minimal Contract Addition

```json
{
  "checkpoint": {
    "status": "needs_user_input",
    "scope": "nested",
    "blocking_skill": "child-checkpoint",
    "stack": [
      {"skill": "parent-checkpoint", "scope": "top_level"},
      {"skill": "child-checkpoint", "scope": "nested"}
    ],
    "status_artifact": "outbox/child/latest-status.json"
  }
}
```

## Conclusion

Nested skills can be treated as checkpoints, but only if checkpoint state is explicitly artifact-backed and surfaced through a structured op output contract with stack-aware resume context.

