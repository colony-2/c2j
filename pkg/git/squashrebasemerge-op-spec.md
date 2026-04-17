# Squash Rebase Merge Op Specification

## Overview

The `squashrebasemerge` git op gives recipes a deterministic pathway to collapse local work into a single commit, rebase onto the latest remote `main`, fast-forward merge the result back to the remote repository, and refresh `GitContext` so the merged tip becomes the new base for subsequent operations. It replaces ad-hoc scripting with a worker-managed flow that keeps thin-pack metadata in sync with remote state.

## Goals

- Provide a turnkey mechanism for recipes to deliver feature work onto a target branch (default `main`) without manual intervention.
- Guarantee thin-pack persistence aligns with the merged remote commit so downstream recipes can restore the updated state.
- Allow callers to preserve original authorship by default while still enabling overrides when necessary.
- Immediately reflect the merged remote tip in `GitContext` so future thin packs and git ops build on the updated base.

## Registration & Usage

- Register `squashrebasemerge` under `server/ops/pkg/git` and expose it through the recipe registry.
- Typical usage:

```yaml
- id: deliver_feature
  op: squashrebasemerge
  with:
    upstream_branch: refs/heads/main
    upstream_repo: git@github.com:org/repo.git
    rebase: true
```

The op inherits the surrounding gitstate lifecycle.

## Inputs

| Field | Required | Description |
| --- | --- | --- |
| `upstream_branch` | yes | Fully qualified ref that will receive the merged commit. Defaults to `refs/heads/main` if omitted. |
| `upstream_repo` | yes | Remote URL/path used to locate the upstream remote for fetch/push. |
| `local_hash` | no | Commit hash to squash (defaults to current `HEAD` when omitted). |
| `rebase` | no | Defaults to `true`; set to `false` to skip the rebase step and only squash onto the existing upstream tip. |
| `author` | no | Author to assign to the squash commit (defaults to the first commit author, with `Co-authored-by` tags for the rest). |
| `commit_message` | no | Commit message to use; defaults to the concatenated commit subjects in the squash range. |

## Execution Flow

1. **Fetch Target** – ensure the latest `upstream_branch` is available locally by fetching from the matched `upstream_repo`.
2. **Merge Base Discovery** – resolve the merge-base between the local hash (or `HEAD`) and the upstream tip to define the squash range.
3. **Squash** – collapse the diff between merge-base and local hash into a single commit with the requested/derived author and commit message.
4. **Rebase** – if enabled, replay the squashed commit onto the fetched tip (`git rebase --reapply-cherry-picks`). Conflicts abort with plain text errors.
5. **Fast-Forward Merge** – push the resulting commit to the remote (`git push <remote> HEAD:<upstream_branch>`), requiring a fast-forward.

## Failure Semantics

- Fetch/push failures bubble up directly for observability and retry logic.
- Rebase conflicts surface as plain text errors; no structured payload is required.
- Push rejection (e.g., remote advanced) aborts the op with a descriptive error so recipes can decide whether to retry or alert an operator.

## Implementation Notes

- Add to exports.go in `server/git/pkg/export`
- Validate that the operation only proceeds when the local history contains changes (no-op squashes should return early).

## Testing Strategy

- **Unit:** cover squash, rebase, and push happy paths plus failure scenarios (conflicts, push rejection).
- **Workflow Integration:**
  - Run within shared and discrete workspaces to confirm isolation semantics are preserved.
  - Chain with `thinpackrebase` to ensure the resulting remote state can be rebased onto by another recipe.
- **End-to-End:** exercise delivery of a feature branch into `main`, validating that thin packs and remote refs match the merged commit.
