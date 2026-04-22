# Plan 4F: Multi-Ref Skill Bootstrap for `codex.exec`

## Goal

Keep `codex.exec` skill-agnostic while making platform skills easy to supply.

The op should only perform environment setup:

1. resolve git refs,
2. copy skill directories into `CODEX_HOME/.agents/skills`,
3. record the resolved git commit hash for each sourced skill bundle,
4. run Codex normally with the user prompt.

## Core principle

No special skill handling in `codex.exec`.

- The recipe/user prompt decides how skills are used.
- Codex CLI behavior decides implicit/explicit skill selection.
- `codex.exec` does not enforce, validate, route, or interpret skill execution.

## Proposed `codex.exec` input addition

```yaml
inputs:
  prompt: "continue"
  skills:
    - github.com/acme/codex-platform-skills/.agents/skills@platform-v12
    - github.com/acme/payments-cell-skills/.agents/skills@9c71eb0d4379a4aa8f4ab94e545e1f53ec94b0b4
```

### `skills` format

Each entry uses a single-line Go-style ref:

`<host>/<org>/<repo>/<skills-root-path>@<git-ref>`

Example:

`github.com/acme/codex-platform-skills/.agents/skills@platform-v12`

## Materialization behavior

For each `skills` entry:

1. Resolve `@ref` to a concrete commit.
2. Fetch/checkout repository content.
3. Copy all subdirectories under `<skills-root-path>` into `CODEX_HOME/.agents/skills`.
4. Record the resolved ref in `skills_installed` for traceability.

That is the full feature scope.

## Installed skill refs behavior

For traceability, `codex.exec` output must include `skills_installed`.

Definition:

1. Each `skills_installed` entry corresponds to one `skills` entry after ref resolution.
2. Each entry uses the same one-line ref format as input:
   `<host>/<org>/<repo>/<skills-root-path>@<resolved-commit>`.
3. `codex.exec` must not compute a new content-derived hash for bundles.

## What is intentionally removed

From `codex.exec`:

1. Remove `skill_mode`.
2. Remove `skill_artifacts`.
3. Remove `skill_blobs`.
4. Remove manual `.c2/skills` sourcing behavior.

Codex skill discovery should rely on native paths:

1. `CODEX_HOME/.agents/skills` (populated from `skills`),
2. current directory and parent `.agents/skills` discovery handled by Codex itself.

## Non-goals

`codex.exec` should not:

1. enforce specific skill names,
2. verify skill execution,
3. add skill-execution-specific output contracts beyond source trace metadata,
4. add precedence/routing semantics beyond basic filesystem setup.

## Observability

`codex.exec` output should record:

1. `skills_installed`: resolved refs in input-compatible format.

No skill-level interpretation is required.

Suggested output shape:

```json
{
  "skills_installed": [
    "github.com/acme/codex-platform-skills/.agents/skills@9c71eb0d4379a4aa8f4ab94e545e1f53ec94b0b4",
    "github.com/acme/payments-cell-skills/.agents/skills@9c71eb0d4379a4aa8f4ab94e545e1f53ec94b0b4"
  ]
}
```

## Example recipe usage (one op, multiple refs)

```yaml
- id: implement
  op: codex.exec
  inputs:
    prompt: "Implement approved plan. Use available skills when appropriate."
    skills:
      - github.com/acme/codex-platform-skills/.agents/skills@platform-v12
      - github.com/acme/payments-cell-skills/.agents/skills@9c71eb0d4379a4aa8f4ab94e545e1f53ec94b0b4
    worktree_path: "{{ context.environment.worktree_path }}"
    cell_relative_path: "{{ context.workflow.cell_path }}"
```

## Migration plan

1. Add `skills` ref support as environment bootstrap only.
2. Move platform skill repos to `.agents/skills/*` layout.
3. Update recipes to replace `skill_blobs` and `skill_artifacts` with `skills`.
4. Remove `.c2/skills` loading and `skill_mode` from `codex.exec`.

## Success criteria

1. One `codex.exec` call can attach multiple skill repos via one-line refs.
2. Recipes no longer carry embedded skill files or artifact key lists.
3. Skill behavior remains entirely Codex-native, not op-specific.
4. `codex.exec` emits `skills_installed` with resolved refs (`...@<resolved-commit>`) for traceability.
