# Separate Git operations

Use `squash`, `rebase`, and `merge` as separate recipe tasks when preparation and
publication should have separate persisted outcomes. `thinpackrebase` and
`squashrebasemerge` remain available with their existing behavior.

| Op | Behavior | Main inputs | Output |
| --- | --- | --- | --- |
| `squash` | Replace local commits after the execution base with one commit; no fetch or push | Optional `base_hash`, `commit_message`, `author` | `squashed_hash`, `squashed_commits` |
| `rebase` | Replay local commits onto a specified base; no squash or push | Required `target_base_hash` | `new_base_hash`, `new_persist_hash` |
| `merge` | Publish the current commit by fast-forward only; no rewriting or merge commit | Optional `upstream_repo`, `upstream_branch` | `merged_hash`, `target_branch` |

All three default `repo_path` to the task worktree. `squash` and `rebase` default
their base to `context.git.execution.base_hash`; `merge` defaults its destination
to `context.git.repo` and `context.git.ref`. `rebase` has the same inputs and
workspace ancestry restrictions as `thinpackrebase`: its target must descend
from the current base and the job's initial commit. It fetches missing history
as necessary, including for shallow workspaces.

```yaml
inputs:
  target_base_hash:
    type: string
sequence:
  # Earlier tasks have made changes, persisted automatically at task completion.
  - id: prepare
    op: squash
    inputs:
      commit_message: "Implement the requested change"
  - id: refresh
    op: rebase
    inputs:
      target_base_hash: "{{ inputs.target_base_hash }}"
  - id: publish
    op: merge
outputs:
  commit: "{{ sequence.publish.outputs.merged_hash }}"
```

Omit `rebase` when the workspace is already based on the intended target.
Omit `squash` to retain individual local commits. Outside an empty workspace,
the target branch must already exist. These ops require committed,
clean worktrees; normal preceding writing tasks persist their changes
as commits. Do not mark these tasks `const`, which disables workspace persistence.

Task completion stores the prepared workspace snapshot before `merge` runs.
A retry of `merge` publishes the same commit; if the remote already points to
that exact commit, publication succeeds as a no-op. A divergent target or one
already ahead of the candidate is rejected. `merge` never force-updates existing
history. It checks target ancestry and fetches missing history when the workspace
is shallow. Publishing to an alternate repository or branch retains the original
workspace base and snapshot. If the target advances, explicitly prepare a new candidate with
`rebase` before publishing again.

For `workspace: {cell: ..., ref: main, initial: empty}`, use `squash` followed by
`merge`, without `rebase`. `squash` creates a root commit and task completion
persists a self-contained snapshot. `merge` creates the target branch only if
absent, or accepts the identical previously published commit. An existing branch
with a different commit is rejected. See [empty workspaces](GUIDE-Empty-Workspaces.md).
