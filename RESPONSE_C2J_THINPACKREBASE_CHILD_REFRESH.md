# Response: thinpackrebase after a child merge

We reproduced and fixed both reported Git failures. The minimal recipe can
continue to supply only the merged target hash:

```yaml
- id: refresh
  op: thinpackrebase
  inputs:
    target_base_hash: '${{ sequence.child.outputs.outputs.merged_hash }}'
```

`base_hash` now defaults to `{{ context.git.execution.base_hash }}` and
`persist_hash` to `{{ context.git.execution.head_hash }}`. These references
resolve after the task restores its workspace. The base is the commit beneath
pending local work; HEAD includes that work. The existing
`context.git.resolved_hash` remains intentionally unresolved early in a job.
Reading the new references does not prematurely pin the job.

Shallow commit availability is no longer mistaken for complete ancestry. The
op fetches target history incrementally to establish the relationship, without
unconditionally unshallowing the repository. Divergence and unavailable history
remain errors. Refresh respects the workspace base constraint.

A successful managed-workspace refresh now records the new persistence base
and preserves pending commits for subsequent tasks and resumed execution. The
no-pending-work case restores directly from the repository base. We also fixed
committer identity setup for rebasing in a fresh worker clone.

Coverage now includes a real child merge followed by native refresh and a
subsequent task, shallow fast-forward/rebase, missing targets, conflicts,
divergence, uncommitted-work protection, already-merged local changes, scoped
workspaces, suspension/resumption, and offline replay. These regressions live
in c2j itself. Previously, the op tests supplied explicit base hashes and full
clones, bypassing both failing integration points.

The reported failure-then-success sequence was a whole-job retry, not replay
losing a recorded result: the next attempt saw the child's completed merge and
took the already-initialized path. That retry policy is unchanged.

Validation: `go test ./...`, focused race-detector tests, the supplied
`native-default-base` reproduction, and all eight cell-setup recipe cases
(including `child-merge-refresh-real-git`).

See [the updated op documentation](pkg/git/thinpackrebase-op-spec.md) for defaults,
outputs, and failure behavior. The fix must reach the runner's c2j executable
before existing deployments can use it.
