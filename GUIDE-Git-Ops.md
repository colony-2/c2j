# Separate Git operations

Use `squash`, `rebase`, and `merge` as separate recipe tasks when preparation and
publication should be explicit in the recipe. `squashrebasemerge` keeps its
single-op recipe API and now performs these primitives as durable internal
tasks, saving the prepared commit before publication. `thinpackrebase` remains
available.

| Op | Behavior | Main inputs | Output |
| --- | --- | --- | --- |
| `squash` | Replace local commits after the execution base with one commit; no fetch or push | Optional `base_hash`, `commit_message`, `author` | `squashed_hash`, `squashed_commits` |
| `rebase` | Replay local commits onto a specified base; no squash or push | Required `target_base_hash` | `new_base_hash`, `new_persist_hash` |
| `merge` | Publish the current commit by fast-forward only; no rewriting or merge commit | Optional `upstream_repo`, `upstream_branch`, `prepared_base_hash` | `merged_hash`, `target_branch` |

All three default `repo_path` to the task worktree. `squash` and `rebase` default
their base to `context.git.execution.base_hash`; `merge` defaults its destination
to `context.git.repo` and `context.git.ref`. `rebase` has the same inputs and
workspace ancestry restrictions as `thinpackrebase`: its target must descend
from the current base and the job's initial commit. It fetches missing history
as necessary, including for shallow workspaces.

```yaml
input_schema:
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
that exact commit, publication succeeds as a no-op. A target containing the
candidate in its ancestry also counts as success, preserving later commits.
A divergent target is rejected. `merge` never force-updates existing
history. It checks target ancestry and fetches missing history when the workspace
is shallow. Publishing to an alternate repository or branch retains the original
workspace base and snapshot. If the target advances, explicitly prepare a new candidate with
`rebase` before publishing again.

## When merge fails because upstream advanced

Use the separate `merge` op when a recipe needs to distinguish a stale candidate
from other publication failures. This contract applies to `merge`; do not assume
the combined `squashrebasemerge` op emits the same code.

When the candidate and target have diverged from the prepared base, `merge`
returns an application error exposed to recipe `catch` as
`failure.kind == 'task_error'` and
`failure.code == 'MERGE_UPSTREAM_ADVANCED'`. The prepared base must be an
ancestor of both commits. Missing history is fetched to establish ancestry;
an unknown base, rewritten ancestry, or an initial-publication collision does
not produce this code.

For the managed task worktree, `merge` uses its execution base automatically.
Standalone calls can supply `prepared_base_hash`; an explicit value takes
precedence. Without a prepared base, divergence retains the ordinary merge
error. This input identifies the base used to prepare the candidate, not the
latest target tip or a newly computed merge base.

The error retains the originating merge node and these `failure.attrs` fields
through persistence and resume:

| Attribute | Value |
| --- | --- |
| `candidate_hash` | Full hash of the prepared candidate |
| `prepared_base_hash` | Full hash of its supplied or managed execution base |
| `upstream_hash` | Full hash of the observed target tip |
| `target_branch` | Full branch ref, such as `refs/heads/main` |
| `phase` | `preflight` or `push` |

A catch on the enclosing merge state can route this failure to your recipe's
existing `rebase` state. This is a state definition to insert under
`state.states`, not a complete recipe:

```yaml
merge:
  sequence:
    - id: squash_reviewed_candidate
      op: squash
    - id: merge_reviewed_candidate
      op: merge
  catch:
    - id: upstream_advanced
      when: >-
        failure.kind == 'task_error' &&
        failure.code == 'MERGE_UPSTREAM_ADVANCED' &&
        failure.node.op == 'merge' &&
        failure.node.id == 'merge_reviewed_candidate'
      to: rebase
      payload:
        merge_failure: '${{ failure }}'
```

The destination state can inspect
`transition.payload.merge_failure.attrs.upstream_hash` and the other diagnostic
attributes. Match the code and originating node, rather than message text.
The node checks keep squash failures and other merge operations out of this
route. Add your normal successful-merge transition or output mapping separately.

The example has no chase limit. For a bounded recovery loop, the recipe must:

1. Keep a durable chase counter, such as `inputs.flow.merge_chases`, and preserve
   it across state transitions, revisions, and child-recipe handoffs.
2. Add a budget check to the catch condition and increment the counter in its
   transition payload. For three chases, check that the count is below three;
   the initial attempt plus those chases allows four stale merge attempts.
3. Resolve the target's current tip again and run native `rebase` against it.
   The error's `upstream_hash` is an observation and may already be stale.
4. Repeat verification and review, including fresh approval when required by
   the recipe, before returning to squash and merge.
5. Let the original failure propagate when the budget is exhausted. Configure
   recipe, enclosing-scope, and job retries consistently with that limit; a
   chase counter alone does not disable other retry policies.

Use existing `catch` ordering to handle the error before retrying the same
candidate. A `retry` on `merge` alone neither rebases nor reruns review. Counter
updates and any required workflow data belong in the recipe's transition payload;
the catch above passes only the failure and does not maintain a counter.

For a standalone checkout, supply the base used to prepare its candidate:

```yaml
id: merge_reviewed_candidate
op: merge
inputs:
  repo_path: '${{ inputs.checkout_path }}'
  upstream_repo: '${{ inputs.repository_url }}'
  upstream_branch: main
  prepared_base_hash: '${{ inputs.prepared_base_hash }}'
```

This node fragment expects those inputs in its enclosing scope. In the managed
task worktree, omit `prepared_base_hash` to use its execution base automatically.

Push races receive the code only when Git reports a non-fast-forward rejection
or a proven concurrent ref update, followed by fresh ancestry verification.
Hook, permission, transport, timeout, and other errors retain their diagnostics.
If the target already contains the candidate, publication succeeds, including
after a lost push response.

The failure preserves the candidate and workspace base for subsequent work.
The recipe owns catch routing, chase limits, rebasing, verification, and fresh
approval. The op does not automatically rebase or retry with a different
candidate. Story error rendering is unchanged.

## Empty workspaces and the combined op

For `workspace: {cell: ..., ref: main, initial: empty}`, use `squash` followed by
`merge`, without `rebase`. `squash` creates a root commit and task completion
persists a self-contained snapshot. `merge` creates the target branch only if
absent, or recognizes the candidate already published in the target's history.
An unrelated existing branch is rejected. See [empty workspaces](GUIDE-Empty-Workspaces.md).

For `squashrebasemerge`, `retry` applies to the failing internal task. Completed
preparation is reused; publication never rebases again on retry. Once these
retries are exhausted, the failure propagates without an automatic enclosing
scope retry that would recreate the candidate. A recipe catch can handle the
failure and explicitly choose further work. Use the managed worktree and do not
mark the op `const`, since the durable boundary requires workspace persistence.

## Git diagnostics

Native Git commands attach their stderr to the task as `_c2j/git-stderr.txt`
when they produce output on that stream. This includes workspace cloning and
source preparation, even when they fail before the operation starts. The artifact
retains command labels and stderr from successful and failed commands; stdout
remains separate. Resolved secrets are redacted before publication. Diagnostics
are bounded to 1 MiB plus an omission marker; oversized command records are
omitted whole. The artifact is retained with the task outcome and during replay.
Child-job results expose the original diagnostic artifact reference rather than
copying the child log into the awaiting task's own diagnostics.
Git commands launched inside shell or extension operations continue to use those
operations' own output artifacts.
