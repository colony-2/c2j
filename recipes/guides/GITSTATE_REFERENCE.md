# GitState Guide for Recipe Authors

This guide explains how recipe steps record git changes, how gitstate flows between steps and recipes, and how `base_ref` and `base_hash` interact when changes are persisted.

## What a recipe author needs to know

Gitstate is the recipe-visible git metadata that keeps steps aligned on the same repo base. It shows up as fields you pass to git-aware ops and as defaults that child recipes inherit:

- `base_repo` - which repo the worktree belongs to
- `base_ref` - symbolic ref (branch/tag) for the intended upstream base
- `base_hash` - resolved commit hash used for deterministic operations
- `author` - commit author string for generated commits

Defaults come from `context.git` (see `TASK_EXECUTION_CONTEXT_REFERENCE.md`).

## How steps record changes

- A step that writes or rebases git history should return updated git metadata so downstream steps continue from the new base.
- Ops that update history expose a `git_context_patch` output (see `ops/OP_THINPACKREBASE.md` and `ops/OP_SQUASHREBASEMERGE.md`). Treat this as the authoritative update after the step persists changes.
- `gha.run` and `gha.runs` execute against disposable snapshots of the current worktree. Workflow file changes are discarded for both `backend: local` and `backend: github`, so these ops should be treated as non-persisting checks or automation runners.
- Steps that only read git do not need to emit updates.

Practical rule: if a step changes the base commit or persist hash, propagate its outputs into the next step’s git inputs (or into child recipe defaults).

## Carrying gitstate through a recipe

Child recipes inherit git defaults when you omit explicit values:

```yaml
defaults:
  git:
    base_repo: "{{ context.git.repo }}"
    base_ref: "{{ context.git.ref }}"
    base_hash: "{{ context.git.resolved_hash }}"
    author: "{{ context.workflow.job_id }}@{{ context.workflow.cell }}"
```

This is the standard way to keep every recipe step aligned on the same git base unless you intentionally override it.

## Reusing outputs to continue from an earlier step

If a step produces a new base/persist hash (for example, a thin-pack rebase), you can use those outputs to continue forward in later steps or even in a new recipe:

- Read the step’s outputs (for example, `new_base_hash`, `new_persist_hash`, or `rebased_from` in `ops/OP_THINPACKREBASE.md`).
- Pass those values into the next op’s git inputs, or set them as defaults when starting a child recipe.

This lets you “branch” a workflow from any prior step that has a persisted output, without re-resolving the upstream ref.

## Base ref vs base hash (and when they resolve)

Use `base_ref` and `base_hash` together:

- `base_ref` names the intended upstream line of history (branch/tag).
- `base_hash` is the actual commit your step should operate on.

Resolution behavior:

- `base_ref` is resolved to `base_hash` once when changes are persisted (job start or a history-changing op).
- Between persist points, downstream steps should keep using the existing `base_hash` rather than re-resolving `base_ref`.
- When a step persists changes and updates the base, it should emit the new hash via `git_context_patch` so later steps advance to the new base.

This keeps the recipe stable during a run while still allowing explicit, persisted updates to move the base forward.

## Where to look

- Git defaults and child recipe propagation: `ops/RUN_RECIPE.md`
- History-changing op outputs: `ops/OP_THINPACKREBASE.md`, `ops/OP_SQUASHREBASEMERGE.md`
- GitHub Actions workflow execution: `ops/OP_GITHUB_ACTIONS.md`

## End-to-end example (continue from a thin-pack rebase)

This example rebases a thin-pack workspace, then starts a child recipe that continues from the rebased base.

```yaml
sequence:
  - id: rebase
    op: thinpackrebase
    inputs:
      repo_path: "{{ inputs.repo_path }}"
      target_base_hash: "{{ inputs.target_base_hash }}"
      upstream_remote: "origin"
      update_refs: "refs/heads/main"
      base_hash: "{{ context.git.resolved_hash }}"
      base_repo: "{{ context.git.repo }}"
      git_author: "{{ context.git.author }}"

  - id: followup
    op: recipe.run_and_get_result
    inputs:
      name: child-continue-from-rebase
      inputs:
        work_mode: "apply"
      git_ref: "{{ context.git.ref }}"
      defaults:
        git:
          base_repo: "{{ context.git.repo }}"
          base_ref: "{{ context.git.ref }}"
          base_hash: "{{ sequence.rebase.outputs.new_base_hash }}"
          author: "{{ context.git.author }}"
```

Notes:
- The child recipe uses `sequence.rebase.outputs.new_base_hash` to continue from the persisted rebase.
- You can also pass `new_persist_hash` (if needed by your workflow) alongside `base_hash` for ops that require it.
