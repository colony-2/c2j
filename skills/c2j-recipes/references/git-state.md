<!-- Sources: pkg/git/thinpackrebase-op-spec.md; pkg/git/squashrebasemerge-op-spec.md; pkg/git/*/wrapper.go; pkg/worker/ops/op_executor.go; pkg/template/template_interpolate.go; pkg/worker/test-fixtures/recipes/job-result-thinpack.yaml. -->

# Git State

Recipes execute against a prepared worktree. Use `context.environment.worktree_path` for host-visible paths and `context.environment.op.worktree_path` inside ops that support op-visible path mapping.

Common git context fields:

- `context.git.repo`
- `context.git.ref`
- `context.git.hash`
- `context.git.resolved_hash`
- `context.git.author`
- `context.git.execution.base_hash` (op input reference, resolved after workspace restoration)
- `context.git.execution.head_hash` (op input reference, actual restored HEAD)

`context.git.resolved_hash` may intentionally be empty before a job commits to a
hash. For op inputs requiring the prepared workspace's hashes, use the execution
references instead. Reading them does not itself pin the job. Use completed op
outputs when recipe routing needs concrete hashes.

## Scoped Workspaces

A node with `workspace` has an independent Git snapshot lineage; descendants inherit it, and the enclosing workspace resumes on exit. `context.git.*` always describes the active workspace. See [workspaces.md](workspaces.md) before passing hashes or thin packs across cell boundaries.

## Persisted Worktree Changes

Commands can write into the worktree and into the op outbox. The worker collects outbox files as artifacts. Git state-aware ops can persist or transform commit state.

```yaml
sequence:
  - id: write_files
    op: command_execution
    inputs:
      working_directory: "{{ context.environment.op.worktree_path }}"
      run: |
        printf 'content' > result.txt
        printf 'artifact' > "${{ context.environment.op.outbox }}/result.txt"
```

## `git_persist_commit`

Use to capture a commit and create a thin pack.

```yaml
op: git_persist_commit
inputs:
  storage_location: "{{ inputs.storage_location }}"
  commit_message: "recipe update"
```

Defaults fill `repo_path`, `root_hash`, and `author` from context.

## `git_restore_commit`

Use to restore a commit, rebuilding from thin packs when needed.

```yaml
op: git_restore_commit
inputs:
  target_commit: "{{ inputs.target_commit }}"
  storage_location: "{{ inputs.storage_location }}"
  force: false
```

## `thinpackrebase`

Use to rebase thin-pack-backed work onto a newer base.

```yaml
op: thinpackrebase
inputs:
  target_base_hash: "{{ inputs.main_head }}"
  preserve_author: true
```

`base_hash` and `persist_hash` default to the task-time execution references;
recipes normally supply only the target. Shallow ancestry is fetched as needed.
A successful refresh preserves pending commits and updates the base used by
subsequent tasks, including after suspension. The target must descend from the
current base; conflicts and genuinely divergent history fail without discarding
pending work. See [the op specification](https://github.com/colony-2/c2j/blob/main/pkg/git/thinpackrebase-op-spec.md).

Outputs include `target_base_hash`, `new_base_hash`, `new_persist_hash`, `updated_ref`, `rebased_from`, and `git_context_patch`.

## Standalone `squash`, `rebase`, and `merge`

Use these when recipe checks, approvals, or other work belong between Git transformations. They are independent built-ins:

| Op | Effect | Main outputs |
| --- | --- | --- |
| `squash` | Replace commits after the workspace base with one local commit. | `squashed_hash`, `squashed_commits` |
| `rebase` | Replay local commits onto `target_base_hash`; same contract as `thinpackrebase`. | `new_base_hash`, `new_persist_hash`, `rebased_from` |
| `merge` | Push the prepared HEAD to the destination branch by fast-forward. | `merged_hash`, `target_branch` |

```yaml
sequence:
  - id: squash_changes
    op: squash
    inputs:
      commit_message: Apply reviewed changes
  - id: refresh_base
    op: rebase
    inputs:
      target_base_hash: "${{ inputs.target_base_hash }}"
  - id: publish
    op: merge
```

Use an existing clean, committed workspace; commit changes before these operations. The worker normally persists ordinary task modifications between steps. `squash` keeps the existing workspace base and defaults `base_hash` to `context.git.execution.base_hash`. In a managed empty workspace it can squash root history without a base.

`rebase` requires a real base and a target descending from it; it does not publish. `merge` defaults the destination to `context.git.repo` / `context.git.ref`, never forces a normal branch update, and fails if the target has diverged. A concurrent publisher can still cause rejection; refresh and revalidate before retrying publication. After publishing to the workspace's own source branch, c2j advances that workspace's base. Publishing elsewhere keeps the source snapshot. An empty workspace can create an absent target branch but does not overwrite an existing unrelated branch.

## `squashrebasemerge`

Use to squash local work, optionally rebase, and fast-forward merge to the upstream branch.

```yaml
op: squashrebasemerge
inputs:
  upstream_repo: "{{ context.git.repo }}"
  upstream_branch: "{{ context.git.ref }}"
  rebase: true
  commit_message: "Apply recipe changes"
```

Outputs include `target_branch`, `remote_ref`, `merged_hash`, `squashed_commits`, `git_context_patch`, and `fast_forward`.

Internally, this op checkpoints squash and rebase before a separate publication
task. Retries reuse the saved candidate and pinned destination; they do not
repeat completed preparation. Publication recognizes the candidate at the
destination tip or in its ancestry and preserves later commits. Exhausted task
retries propagate without automatically restarting the enclosing scope. Use
the managed worktree with persistence enabled (`const: false`).

## Practical Rules

- Prefer context defaults for repo paths and refs unless the recipe intentionally targets another checkout.
- Use full hashes when invoking git ops with hash validation.
- Preserve author metadata unless the recipe explicitly acts as a bot or release process.
- Branch on git op outputs instead of parsing command output when a built-in git op exists.
