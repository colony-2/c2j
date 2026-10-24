# Task-scoped delegation deadline enforcement

Status: resolved in JobDB revision
`6ae6faa00896d49ba1f89467c84c7ca8a0bb1863`.

The requirement was to bound delegated credentials by the authoritative execution
budget, including time consumed by preparation. Repeated issuance must not
restart that budget, and renewal must not extend it.

JobDB solves this through `workflow.TaskContext.CreateDelegation(ctx, request)`.
The live task context retains an engine-owned delegation capability. Creation
applies the earliest task attempt, task total, enclosing scope, job, execution
caller, parent capability, and explicitly requested deadline. Absent bounds are
ignored. With no applicable deadline, authority lasts only while the exact parent
lease remains live.

The method's `ctx` controls cancellation of issuance; its request timeout does
not become a credential deadline. An additional lifetime limit belongs in
`CreateDelegationRequest.NotAfter`. Creation through the remote runtime uses local
attenuation without a network request or execution-history write.

A copied live task context preserves issuance capability. Manually reconstructed
contexts and unsupported runtimes return `ErrLeaseDelegationUnsupported`. Closed
task contexts cannot issue more credentials. Already-issued children remain
subject to their fixed deadline and parent liveness; task completion alone does
not individually revoke them. `Renew` and `KeepAlive` validate authority without
extending the deadline or renewing the parent.

This satisfies the requirement without exposing an effective-deadline accessor.
No additional JobDB API or deadline patch is requested.
