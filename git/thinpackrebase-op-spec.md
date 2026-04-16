# Thinpack Rebase Op Specification

## Overview

The `thinpackrebase` git op rebases a recipe’s thin-pack backed history onto a newer base commit (typically the head of `main`) and republishes a thin pack representing the rebased commits.

## Goals

- Provide a first-class op that rebases the active `GitContext` onto a caller-specified base hash.
- Preserve commit lineage, metadata, and thin-pack artifacts without requiring users to leave the recipe workspace.
- Keep defaults backwards compatible by reusing existing author metadata and workspace semantics (shared or discrete) automatically.

## Registration & Usage

- Register `thinpackrebase` under `server/git/pkg/thinpackrebase` and surface it through the recipe registry.
- Typical recipe snippet:

```yaml
- id: refresh_main
  op: thinpackrebase
  with:
    target_base_hash: {{ context.inputs.main_head }}
    preserve_author: true
```

## Inputs

| Field | Required | Description |
| --- | --- | --- |
| `target_base_hash` | yes | Commit hash that becomes the new base (often the latest `main`/`trunk`). |
| `upstream_remote` | no | Remote name to fetch the target base from; defaults to the remote encoded in `context.git.base_repo`. |
| `preserve_author` | no | Defaults to `true`; when set to `false` the worker reassigns commits to the workflow’s default author identity. |
| `update_refs` | no | Optional refspec (e.g., `refs/heads/feature`) to fast-forward after rebasing. Defaults to detached HEAD.

## Execution Flow

1. **Workspace Preparation** – reuse the prepared workspace and ensure the configured remote contains `target_base_hash`.
2. **Baseline Validation** – verify the current `persist_hash` is a descendant of `target_base_hash`. Fail fast if history diverged.
3. **Rebase** – run `git rebase --reapply-cherry-picks` (or libgit2 equivalent) from `context.git.base_hash` onto `target_base_hash` while respecting the `preserve_author` flag.
4. **Thin Pack Generation** – invoke `gitcommit.PersistCommit` to write a new thin pack for the rebased commits. Filename incorporates the new base hash for traceability.
5. **Context Update** – set `context.git.base_hash = target_base_hash`, refresh `context.git.persist_hash` to the rebased tip, and append a `rebased_from` block detailing the old base/persist hashes.
6. **Output Injection** – update recipe outputs so downstream nodes read the refreshed `context.git` and `git_persist_hash` values.

## Failure Semantics

- Rebase conflicts produce plain text activity errors describing the failing command. Callers can branch on standard error strings when needed.
- Retries reuse the same workspace; the op detects when the target base moves again to avoid replaying conflicting rebases.
- Thin-pack upload failures retry the upload step without repeating the entire rebase (the rebased commit already exists locally).

## Testing Strategy

- **Unit:**
  - Verify happy-path rebase, conflict detection, and thin-pack regeneration within `ops/thinpackrebase` tests.
  - Confirm the controller updates `GitContext` fields and outputs correctly.
- **End-to-End:**
  - Exercise a recipe that periodically rebases onto moving `main` while other activities mutate the workspace, validating blob-store contents and commit metadata.

## Rollout

- Update user documentation (`VIBETHIS.md`, cookbook) with examples and troubleshooting tips.


## Notes

If it makes sense, extract some common context variables from server/recipe-worker to the server/git packages to avoid duplication.