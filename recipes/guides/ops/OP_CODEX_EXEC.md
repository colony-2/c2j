# codex.exec Op

Runs Codex CLI non-interactively and returns normalized status/summary.

This op also emits output artifacts:
- `stdout.jsonl`
- `stderr.txt`

## Inputs

Common fields:
- `prompt` (required): prompt sent to Codex.
- `sessionId`: resume an existing Codex session.
- `model`: Codex model override.
- `env`: extra environment variables.
- `worktree_path` (required): git worktree root.
- `cell_relative_path` (required): writable cell path relative to worktree.
- `workdir_path`: operation workdir root.
- `artifact_inbox_path`: operation inbox path.
- `artifact_outbox_path`: operation outbox path.

Skill-related fields:
- `skill`: optional top-level skill to enforce for this invocation.
- `skills`: list of skill source refs in format `<host>/<org>/<repo>/<skills-root-path>@<git-ref>`.
- `skill_mode`: currently supports `enforce`.
- `skill_selection_mode`: `adaptive` or `ordered` (default `adaptive`).
- `return_on`: checkpoint statuses that should cause `incomplete` return.
- `status_contract.path`: outbox-relative status JSON path (for example `implementation/latest-status.json`).

Notes:
- `skills` is for installing skill bundles.
- `skill` is for selecting/enforcing one top-level skill segment.
- `skill_artifacts` and `skill_blobs` are not supported.

## Skill Source Resolution

When `skills` refs are provided, `codex.exec`:
1. Resolves each ref to a concrete commit.
2. Fetches/checks out repository content for that commit.
3. Copies all subdirectories under the referenced skills root into the invocation skill staging area.
4. Emits resolved refs in `skills_installed` output.

Merged precedence during execution:
- repo `.c2/skills` overrides configured ref sources
- configured ref sources override codex-home copied from inbox

## Example: Basic

```yaml
- id: run_codex
  op: codex.exec
  inputs:
    prompt: "Summarize the changes in this repo."
    worktree_path: "{{ context.environment.worktree_path }}"
    cell_relative_path: "{{ context.workflow.cell_path }}"
```

## Example: Skill Sources via Git Refs

```yaml
- id: run_codex_skill
  op: codex.exec
  inputs:
    prompt: "Use my-skill."
    skill: "my-skill"
    skill_mode: "enforce"
    skills:
      - "github.com/acme/codex-platform-skills/.agents/skills@platform-v12"
      - "github.com/acme/payments-cell-skills/.agents/skills@main"
    worktree_path: "{{ context.environment.worktree_path }}"
    cell_relative_path: "{{ context.workflow.cell_path }}"
```

## Outputs

Top-level output:
- `status`: `completed | incomplete | error`
- `sessionId`
- `assistantSummary`
- `incompleteReason`
- `incompleteCategory`
- `pendingDependencies`
- `skills_installed`: resolved refs in input-compatible format (`...@<resolved-commit>`)
- `outcome` (structured checkpoint/skill/routing metadata)

Example:

```json
{
  "status": "incomplete",
  "sessionId": "session-id",
  "assistantSummary": "Need user input",
  "incompleteReason": "needs_user_input",
  "incompleteCategory": "needs_user_input",
  "pendingDependencies": [],
  "skills_installed": [
    "github.com/acme/codex-platform-skills/.agents/skills@9c71eb0d4379a4aa8f4ab94e545e1f53ec94b0b4"
  ],
  "outcome": {
    "summary": {
      "human": "Need user input",
      "reason": "awaiting response"
    },
    "skill": {
      "executed": "my-skill",
      "selectionMode": "adaptive",
      "nextCandidate": ""
    },
    "checkpoint": {
      "status": "needs_user_input",
      "scope": "top_level",
      "blockingSkill": "",
      "stack": [
        {
          "skill": "my-skill",
          "scope": "top_level"
        }
      ],
      "statusArtifact": "implementation/latest-status.json",
      "returnTriggered": true,
      "returnReason": "needs_user_input",
      "contractErrors": []
    },
    "routing": {
      "nextAction": "return_to_recipe_checkpoint"
    }
  }
}
```
