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
  inputs:
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

The recipe API and final output fields remain unchanged. Internally the op has
three worker tasks, reusing the standalone squash, rebase, and merge primitives:

1. **Prepare/squash** – fetch the requested destination, pin its exact tip, discover the merge base, and squash the local range. Save the original range, pinned destination, candidate SHA, and workspace snapshot before continuing. Empty workspaces prepare a root commit without fetching.
2. **Rebase** – replay onto the saved target SHA, then persist the candidate and snapshot. When rebasing is disabled or the workspace has an empty base, this task passes through the prepared candidate. No-change workspaces fast-forward to the pinned tip.
3. **Merge** – verify that restored HEAD equals the saved candidate and publish that exact SHA. No commits are created here. No-change flows complete without a push.

Managed worktree paths are represented by a sentinel between tasks, so a new
worker restores the same snapshot at its own path. Normal publication retains
the prepared snapshot as result/child evidence; first publication transitions
the workspace out of empty-base mode.

## Failure Semantics

- Fetch/push failures bubble up directly for observability and retry logic.
- Rebase conflicts surface as plain text errors; no structured payload is required.
- A lost push response or outcome is reconciled against the exact saved candidate. If the branch equals or contains that commit, publication succeeds without changing later history. Otherwise a normal push must fast-forward; initial publication requires an absent branch.
- `retry` applies per internal task. Exhausted retries propagate as a non-retryable enclosing-scope failure, avoiding automatic re-preparation after an ambiguous publication. Recipe catches can handle the failure explicitly.
- A divergent target requires explicit new preparation, rather than an implicit rebase during publication retry.
- Use the managed worktree with persistence enabled. Direct Go `Run` calls execute the same primitives synchronously and do not provide worker checkpoints.

## Implementation Notes

- `wrapper.go` registers the task chain; `chain.go` carries its checkpoint state.
- No-change workspaces complete without publishing a new commit.

## Testing Strategy

- **Unit:** cover squash, rebase, and push happy paths plus failure scenarios (conflicts, push rejection).
- **Workflow Integration:**
  - Run within shared and discrete workspaces to confirm isolation semantics are preserved.
  - Chain with `thinpackrebase` to ensure the resulting remote state can be rebased onto by another recipe.
- **End-to-End:** exercise delivery of a feature branch into `main`, validating that thin packs and remote refs match the merged commit.
- **Failure injection:** lose outcomes after each step, discard worktrees, lose a successful push response, fail the JobDB outcome write and resume on a new worker, and advance or diverge the target before retry. Cover first publication and existing branches.
