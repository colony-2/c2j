# Repo-Root GitState Persist/Restore

## Current Model

The target cell is the root of the target repository. Automatic git persistence
therefore treats the full worktree as the unit of work.

`gitstate.Controller` resolves its persistence scope to `"."` and delegates to
the existing git helpers:

1. Before persist, inspect the repo-root worktree for changes.
2. Persist all tracked, modified, deleted, and untracked files under the repo
   root.
3. After persist or restore, reset and clean the repo-root worktree so
   `git status --porcelain` is empty.

This intentionally removes the older subtree-scoped behavior. There is no
separate repo-relative cell directory in runtime context.

## Testing Strategy

Controller tests should verify that:

1. Repo-root changes are included in automatic persistence.
2. Restore leaves the full worktree clean.
3. Explicit git persist/restore activities continue to work unchanged.
