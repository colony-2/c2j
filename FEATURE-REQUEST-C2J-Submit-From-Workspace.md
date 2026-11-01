# Submit from the current workspace

## Problem

`c2j submit` starts from the target cell's configured Git ref. It cannot opt into
the current task's starting workspace state or its latest local changes. Recipe
child-start operations already support inheriting a persisted workspace snapshot;
CLI submission needs an equivalent capability, with live capture as a separate
opt-in.

## CLI

```bash
c2j submit "Review this task's starting state" --from-workspace
c2j submit "Continue with my latest changes" --from-workspace-now
```

Add two mutually exclusive boolean flags:

- `--from-workspace`: reuse the immutable workspace state supplied at the start
  of the current task invocation. This is the active workspace's incoming state,
  not the parent job's original state. Exclude edits and commits made during the
  current task; do not inspect or snapshot its live worktree.
- `--from-workspace-now`: use the workspace's current state at submission time.
  Reuse an existing snapshot when it represents that state; create a new snapshot
  when local changes require one.

Without either flag, submission behavior remains unchanged. Both work with the
default build recipe and custom recipes.

Outside a running c2j task, `--from-workspace` fails with guidance to use
`--from-workspace-now`, since no task-start snapshot exists. The latter supports
an ordinary local Git worktree, using its current HEAD and file contents.

## Behavior

- Inside a running task, use its active workspace identity and incoming snapshot.
  For live capture outside a task, locate the Git worktree containing the current
  working directory.
- For `--from-workspace-now`, include local HEAD, staged and
  unstaged tracked changes, deletions, and non-ignored untracked files. Ignored
  untracked files are excluded. The child receives the resulting file tree;
  staging boundaries are not preserved. A clean worktree with new local commits
  must use its current HEAD, even if it differs from the task-start snapshot;
  create transport artifacts as needed without creating an unnecessary commit.
- Submit a durable snapshot using the existing Git snapshot/thin-pack and restore
  machinery. Include the base identity and all objects needed to restore local
  commits and edits on another worker without pushing them first.
- Start the job's root workspace from that snapshot. Later changes to the source
  worktree do not affect the submitted job; retries restore the submitted state.
- Leave the submitting worktree, index, HEAD, and branch refs unchanged. Do not
  push or merge as part of submission.
- Preserve existing recipe resolution, inputs, artifacts, and parent-job linkage.
  Explicit recipe workspace overrides continue to select their own workspaces.

With either flag, an omitted target or `--self` selects the selected workspace's
cell. An explicit `--cell` must resolve to the same repository and cell identity;
reject a mismatch rather than applying the snapshot to another cell.

## Implementation outline

Reuse or extract the snapshot creation and child workspace initialization logic.
Carry the selected snapshot artifact, initial commit, and workspace metadata
through direct submission and the child-job broker. Bind the task-start snapshot
to the broker's invocation context for `--from-workspace`. For
`--from-workspace-now`, capture live worktree contents, including edits made during
that operation, without advancing the parent task's persisted workspace state.

Snapshot creation and artifact preparation must succeed before creating the job.
Return an actionable error for missing task-start context, unavailable snapshot
artifacts, target mismatch, or capture failure. Live capture additionally rejects
a missing Git worktree or unresolved merge conflicts and requires a worktree that
is not being concurrently modified. Live worktree dirtiness or conflicts do not
prevent reuse of a valid task-start snapshot. Never silently fall back to the
configured ref.

## Acceptance criteria

- `--from-workspace` reproduces the active workspace at task entry, including
  prior tasks' persisted changes, while excluding the current task's changes.
- `--from-workspace-now` includes unpublished commits, staged and unstaged edits,
  deletions, and non-ignored new files. An unchanged workspace reuses its snapshot;
  a clean worktree with a newer HEAD starts from that HEAD.
- Submission leaves local Git state unchanged and requires no push.
- Both modes retain parent-job linkage inside a running operation. Only
  `--from-workspace-now` includes changes made earlier in that operation.
- Subsequent local edits, worker restart, and job retry do not change the captured
  starting state.
- Ordinary local submissions support `--from-workspace-now`; task-start mode
  requires invocation context. Supplying both flags is an error.
- Target mismatches, unavailable snapshots, and capture failures create no
  runnable job.
- Submissions without either flag retain their existing behavior.
