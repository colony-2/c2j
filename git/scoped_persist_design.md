# Scoped GitState Persist/Restore

## Problem Statement

The recipe worker runs `Controller.Persist` for every inline recipe invocation (`server/recipe-worker/pkg/gitstate/workspace_controller.go`), which calls `gitcommit.PersistCommit`. The implementation at `server/git/pkg/gitcommit/persist.go` stages the entire repository via `git add -A`, so any stray edits inside the worktree join the synthetic commit. During restore we checkout the requested commit, but we never clean files that live outside the recipe's cell. When two cells share a workspace, automatic persistence leaks unrelated changes and the subsequent restore leaves a dirty tree.

Automatic persistence needs to focus solely on the active cell. Everything else must be discarded before we record the commit and guaranteed clean after restore.

## Desired Behaviour

1. Resolve the active cell's repo path from the existing cell name metadata that already flows through the git context.
2. Before persisting, remove staged/unstaged changes that do not live under the cell path; keep only the cell subtree.
3. After restore, the worktree must exactly match the restored commit (`git status --porcelain` returns empty) and there must be no residue outside the cell.
4. The explicit `gitcommit` persist/restore activities continue working as-is; only the automatic recipe-worker lifecycle gets the scoped behaviour.

## Scope Resolution

`gitstate.Context` already carries `CellName` and the invocation metadata includes the recipe node path. We can leverage the graph service (or cached cell metadata) to resolve a repo-relative directory from the cell name. The controller should:

- Add a helper `resolveCellPath(ctx Context) (string, error)` that looks up the cell definition (e.g. via `server/graph` APIs or an injected resolver) and returns the repo-relative path.
- Cache the mapping per workspace to avoid repeated lookups.
- Fail fast when the cell cannot be resolved; automatic persistence cannot proceed without a deterministic path.

No additional fields are required on the public git context – the controller derives the absolute path on demand by combining the repo root returned by `ws.GetWorktreePath()` with the resolved repo-relative cell path.

## Persist Workflow Adjustments

All changes stay inside `server/recipe-worker/pkg/gitstate/workspace_controller.go:160` before we invoke `gitcommit.PersistCommit`.

1. **Clean staging area** – execute `git reset --mixed` so prior staging does not bleed into the scoped commit.
2. **Detect foreign changes** – list tracked and untracked changes outside the cell directory (e.g. `git status --porcelain -- :".^{cell}"`) and remove them:
   - Tracked: `git restore --worktree --staged -- "":^cellPath` to drop modifications outside the path.
   - Untracked: `git clean -fd -- "":^cellPath` to delete new files elsewhere.
   - If we encounter tracked edits outside the cell we will log and remove them automatically to preserve the previous automation semantics.
3. **Ensure scoped staged set** – explicitly stage the cell path (`git add -- cellPath`). After this point `git status --porcelain` should report only the cell subtree.
4. **Short-circuit** – if the scoped path produces no changes, skip calling `PersistCommit` and return the existing persist hash, mirroring the current behaviour for empty commits.
5. **Call existing persist** – once the workspace contains only the cell changes, invoke `gitcommit.PersistCommit` as today. Because other paths were cleaned, the commit contains only cell files even though `git add -A` runs internally.
6. **Telemetry** – emit structured logs (cell name/path, counts of files cleaned/staged) to aid debugging.

These steps can live in a small helper (e.g. `prepareScopedPersist(repoRoot string, cellPath string) error`) that uses the `common.ExecuteGitCommand` helper for consistency.

## Restore Workflow Adjustments

After `gitcommit.RestoreCommit` returns in `workspace_controller.go:120`:

1. Run `git reset --hard <persistHash>` to guarantee tracked files align with the restored commit.
2. Run `git clean -fd -- ":^${cellPath}"` so we drop untracked files outside the cell tree (the restored commit already contains tracked cell files).
3. Optionally stage the status check: if `git status --porcelain` is not empty, return an error – the restore contract is to produce a clean tree.
4. Log any files that needed cleaning so operators can trace unexpected leakage.

`gitcommit.RestoreCommit` remains untouched; the controller enforces cleanliness post-restore the same way it preps the tree pre-persist.

## Data Plumbing

- Teach the controller to resolve the cell path when `Workspace.GetCellName()` is non-empty and memoize it inside the workspace context (e.g. `Context` can store a transient `ResolvedCellPath` field set by the controller; it does not need to flow through external APIs).
- Inline and detached workspace derivation already propagate `CellName`; no additional payload changes are required.

## Testing Strategy

1. Add controller-level tests in `workspace_controller_test.go` that simulate two cell directories with overlapping workspaces. Verify that persisting one cell cleans edits in the other and that restore produces a clean `git status`.
2. Add end-to-end inline workspace tests (`inline_workspace_test.go`) that create files in and out of the cell directory to confirm only the cell files get committed.
3. Include a regression test that ensures the explicit `gitcommit` activities remain unchanged when invoked directly (no scope cleanups occur).
4. Exercise the failure path where cell resolution fails – the controller should return a descriptive error.

## Rollout Notes

- Implement the cell-path resolver using existing graph metadata; keep it configurable via dependency injection for tests.
- Roll out behind a recipe-worker flag so we can verify in staging before enabling globally.
- Update `server/git/AGENTS.md` to mention the scoped persistence behaviour and the requirement that cell names map to deterministic paths.

## Open Questions

- How do we resolve cells whose source lives outside the repository (symlinks, generated outputs)? The controller may need to support a fallback path override once we encounter such a cell.
- Should we emit warnings instead of hard failures when non-cell changes are wiped, or is logging enough? The current plan preserves automation by cleaning silently but logs the cleanup.

