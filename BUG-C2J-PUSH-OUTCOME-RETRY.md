# Publication can succeed before its task outcome is recorded

Status: fixed for the recipe `squashrebasemerge` task chain and standalone `merge`.

The combined op previously created commits and pushed in one task. A successful
push followed by a lost response or task outcome could cause a retry to recreate
commits against changed history.

The recipe API remains `op: squashrebasemerge`, with unchanged inputs and final
outputs. Preparation and rebase now each persist a workspace snapshot before
publication. The publication task verifies and pushes the saved candidate SHA;
it cannot recreate the commit. Recipe retries apply to the failed task, and an
exhausted task failure prevents enclosing automatic retries from repeating
preparation.

Both publication paths recognize the exact candidate at the destination tip or
in its ancestry. Later commits are preserved. Divergent history is rejected.
Initial publication still uses the branch-absence lease, with reconciliation
after a failed push so repository creation can happen during receive-pack.

Regression coverage:

- `pkg/worker/compiler/publication_recovery_test.go`: lost outcomes after every
  internal step, discarded worktrees, initial and existing branches, subsequent
  writers, divergence, replay, and an actual injected JobDB outcome-write failure
  recovered by a newly constructed worker.
- `pkg/git/squashrebasemerge/publication_transport_test.go`: real smart HTTP
  accepts the push, loses its response, and remains unavailable for immediate
  reconciliation; a fresh checkout then retries the saved candidate.
- `pkg/git/squashrebasemerge/chain_test.go`: the target stays pinned across steps
  and a changed local HEAD cannot substitute for the saved candidate.
- `pkg/worker/compiler/execution_scopes_integration_test.go`: op timeouts and
  exhausted task timeouts cannot restart preparation through an enclosing retry.

Durability requires the managed workspace and persistence (`const: false`).
Direct Go `Run` calls are synchronous and have no task checkpoints.
