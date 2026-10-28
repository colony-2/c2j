<!-- Sources: pkg/git/thinpackrebase-op-spec.md; pkg/git/squashrebasemerge-op-spec.md; pkg/git/*/wrapper.go; pkg/worker/ops/op_executor.go; pkg/template/template_interpolate.go; pkg/worker/test-fixtures/recipes/job-result-thinpack.yaml. -->

# Git State

Recipes execute against a prepared worktree. Use `context.environment.worktree_path` for host-visible paths and `context.environment.op.worktree_path` inside ops that support op-visible path mapping.

Common git context fields:

- `context.git.repo`
- `context.git.ref`
- `context.git.hash`
- `context.git.resolved_hash`
- `context.git.author`

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

Outputs include `target_base_hash`, `new_base_hash`, `new_persist_hash`, `updated_ref`, `rebased_from`, and `git_context_patch`.

## Separate `squash`, `rebase`, and `merge`

Use separate operations when verification or review must occur between preparation
and publication. `squash` prepares one local commit; `rebase` accepts
`target_base_hash`; `merge` publishes the current candidate by fast-forward.
All default `repo_path` to the task worktree. Keep workspace persistence enabled.

The separate `merge` op emits `failure.kind == 'task_error'` and
`failure.code == 'MERGE_UPSTREAM_ADVANCED'` only when ancestry proves that the
target advanced from the candidate's prepared base and publication cannot
fast-forward. It covers preflight and identifiable push races. A target already
containing the candidate counts as success.

Managed workspaces provide the execution base automatically. Standalone calls
can supply `prepared_base_hash`; without a trustworthy base, divergence remains
an ordinary error. Do not substitute the latest upstream tip for the prepared base.

Use `catch` to match the code and the original merge node's `id` and `op`.
`failure.attrs` contains `candidate_hash`, `prepared_base_hash`, `upstream_hash`,
`target_branch`, and `phase` (`preflight` or `push`), retained through persistence
and resume. Do not parse error messages or story labels for routing.

The recipe owns rebase routing, durable chase counts, fresh verification and
approval, and exhaustion behavior. Resolve upstream again before rebasing;
the observed hash may be stale. Preserve the count across revisions and handoffs,
and account for enclosing/job retries when bounding attempts. Authentication,
hooks, transport failures, rewritten ancestry, and squash failures must propagate
without consuming a chase. Do not assume `squashrebasemerge` emits this code.

See the [user guide and catch example](../../../../GUIDE-Git-Ops.md#when-merge-fails-because-upstream-advanced).

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

## Practical Rules

- Prefer context defaults for repo paths and refs unless the recipe intentionally targets another checkout.
- Use full hashes when invoking git ops with hash validation.
- Preserve author metadata unless the recipe explicitly acts as a bot or release process.
- Branch on git op outputs instead of parsing command output when a built-in git op exists.
