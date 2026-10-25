# Execution-aware JobDB runtime

Wrap a persistent JobDB runtime with `executionruntime.New(runtime, allocation,
onHandoff)`. Pass the same normalized allocation and callback to
`compiler.RecipeJobWorkerOptions`, with `StageExecution: wrapped.Stage` and
`WrapTaskWorker: wrapped.WrapTaskWorker`, and
build the engine using the wrapped runtime. Register schemas using the original
runtime. The CLI performs this wiring for all execution entry points.

The recipe reconstructs requirements from its input and retained activity results
on every invocation. It never restores execution state from client payload.
The compiler stages effective requirements before each task call. Guarded task
workers check them only after JobDB decides the task is unfinished. Satisfied
requirements allow execution; a mismatch publishes the requirements and yields.
Cached tasks never request historical environments, including when recovery
starts through a stale task route.

Wrap every task worker with `WrapTaskWorker` and supply that same function in
the recipe worker options. Resource and image constraints require this guard;
staging alone cannot enforce a live-task boundary.

Node scopes are ephemeral. An ordinary task/time reschedule publishes the staged
effective requirements if they differ from the saved scheduling hint. Submission
overrides and durable activity changes take precedence over recipe and node
declarations. Replaying directives only rebuilds in-memory state; it never
unconditionally yields. Publication uses JobDB's payload revision for concurrency
and preserves unrelated client JSON. No separate checkpoints or demand revision
are needed.

Stop or redirect an incompatible worker after `OnHandoff` to avoid a tight
reacquisition loop. The event is a handoff, not completion. Admission errors
stop keepalive for rejected leases; ordinary lease-expiry recovery applies.

Use `ops.SuspendExecution` at a successful durable activity boundary to request
a change. Read-only replay must use `ReadOnlyReplay: true` and must not publish.
See the [design](../../C2J_PORTABLE_EXECUTION_REQUIREMENTS_DESIGN.md) and
[user guide](../../GUIDE-Execution-Tracking.md) for lifecycle and failure semantics.

## Supplied leases

`Runtime.RunWithLease(ctx, lease, request, listener)` uses JobDB's supplied-lease
runner and checks live tasks after authoritative lease validation/renewal.
It never calls acquisition APIs. Keep the same `StageExecution`,
`WrapTaskWorker`, allocation, and callback wiring described above. The method
returns after one lease lifecycle, including a normal reschedule or handoff.

Renewable lease wrappers retain shared invocation state across refreshed snapshots,
so a heartbeat cannot drop staged requirements before rescheduling. They forward
current token, owner, expiry, and schema metadata. See the
[run-with-lease guide](../../GUIDE-Run-With-Lease.md) for usage and deployment details.
