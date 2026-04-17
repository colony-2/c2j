# Git Ref-First Context Migration

We need Git contexts to carry arbitrary refs (branches/tags/SHAs) until a step actually produces a commit. Today the contexts are hash-only (`BaseHash`, `PersistHash`, `PreviousHash`, `ParentHash`), which forces early pinning to SHAs and always generates thin packs even when no changes occur. This document specifies the ref-first model, the field renames, and the required call-site updates.

## Goals
- Allow recipes to start from a ref (e.g., `main`) and keep passing that ref through ops that make no changes.
- Only materialize commit hashes and thin packs when a persist produces a new commit.
- Avoid generating empty thin packs and return the input ref unchanged when nothing changed.

## Context Field Renames and Semantics

### GitBaseContext (recipe-core/pkg/contextual)
- `BaseHash` → `BaseRef`: checkout target (branch/tag/SHA) for the initial clone.
- `GitAuthor` unchanged.
- Add optional `ResolvedBaseHash` (string) to capture the SHA resolved from `BaseRef` during workspace prep; populated once and reused by thin-pack operations.

### GitCommitContext (recipe-core/pkg/contextual)
- Single ref slot: `ParentRef` carries the ref while no commit exists.
- Add `PersistHash`/`ParentHash` (materialized) to carry SHAs once a commit is created; used for thin-pack naming and rebase helpers. Exclusivity: either `ParentRef` is set (ref-first mode) **or** both hashes are set (hash mode), never both.

### GitTaskContext (git/pkg/gitstate)
- Mirror the above rename:
  - `BaseHash` → `BaseRef`
  - `PreviousHash` → `PreviousRef` (ref slot used for parent)
- Add `ResolvedBaseHash`, `PersistHash`, `ParentHash` (materialized) to store SHAs once resolved/created. Exclusivity: `ParentRef` set before first commit, hashes set after commit.
- Getter methods and JSON wiring in all consumers must align with the new names.

### Call-Site Inventory (must be updated)
- `recipe-core/pkg/contextual/context.go` struct definitions and JSON tags.
- `git/pkg/gitstate`: `GitTaskContext`, getters, `Controller`, `metadata.go`.
- `git/pkg/gitshallow`: accept refs for clone/checkout.
- `git/pkg/gitcommit`: persist/restore inputs and outputs.
- `git/pkg/workspace/*`: child/detached contexts and defaults.
- `recipe-worker/pkg/compiler`: `NewGitTaskContext`, `UpdateGitState`.
- `recipe-worker/pkg/ops`: `ActivityInvocation{Request,Output}` envelopes and registry wrapper.
- `recipe-template/pkg/template`: resolution context uses of `GitCommitContext`.
- Tests/fixtures/docs: `recipe-worker` fixtures, `workflowctl-start-recipe-spec.md`, `context-model-plan.md`, `git` specs referencing hash-only fields.

## Behavioral Changes

- **Workspace prep / restore**
  - Accept `BaseRef` instead of assuming a SHA. Resolve `BaseRef` to a SHA during the first clone/restore (`git rev-parse`), store it in `ResolvedBaseHash`, and reuse it for thin-pack roots.
  - `Restore` should detect whether we have a materialized `PersistHash`. If only refs are present, skip thin-pack lookup/apply and simply `fetch/checkout` the ref; keep contexts unchanged.
  - When restoring a branch/tag ref, always fetch and move to the latest tip of that ref even if the local clone has an older commit (ref checkout must track upstream, not the local snapshot).
  - After any fetch/checkout, refresh `ResolvedBaseHash` to the resolved SHA for traceability and to seed thin-pack operations once a commit is made.

- **Persist**
  - Persist must accept refs. Before committing, resolve the current HEAD SHA; when changes exist, create the commit and set `ParentHash` to the prior SHA, `PersistHash` to the new commit SHA (clear `ParentRef`), and write thin pack using `ResolvedBaseHash`.
  - When **no changes** exist: do not create a thin pack or placeholder file; return the input `ParentRef` unchanged, leave hashes empty, and do not flip the context into hash mode.
  - Output structs should surface either the ref (pre-commit) or the hashes (post-commit) so downstream ops can use thin packs after the first write.

- **Propagation**
  - `ResolutionContext.UpdateGitState` and all activity outputs should move the ref fields, not the hash-only names.
  - `UpdateGitState` (recipe-template) should also accept and store the materialized hash fields when present so downstream scopes can see both refs and hashes.
  - Any logic that compares hashes (e.g., `hashesEqual`, thin-pack naming) must guard against ref strings and prefer materialized hashes when available.

## Migration Steps
1. Rename fields in the three contexts, adjust JSON tags, and add the optional materialized hash fields.
2. Update constructors (`NewGitTaskContext`), getters, and all call sites in compiler/ops/template code to the new names.
3. Teach gitstate controller and gitshallow clone to accept refs, resolve hashes lazily, and store `ResolvedBaseHash`.
4. Update persist flow:
   - Make `gitcommit.PersistCommit` accept refs, resolve hashes internally.
   - Skip thin-pack creation when no changes; propagate the input ref as output.
   - Return both ref and hash fields when a commit is created.
5. Adjust restore logic to branch between ref checkout (pre-commit) and thin-pack apply (post-commit).
6. Refresh docs/specs/tests to reflect ref-first naming and behaviors (no empty thin packs).

## Success Criteria
- Recipes can be launched with a branch name; ops that make no changes return the same ref and do not emit thin packs.
- First mutating op produces a commit SHA, switches contexts into hash/thin-pack mode, and subsequent ops operate on hashes.
- Persist with no changes returns the input ref and leaves blob storage unchanged.
- All tests and specs refer to the new field names.
