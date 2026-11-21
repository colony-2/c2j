# Execution-aware JobDB runtime

Wrap a persistent JobDB runtime with `executionruntime.New(runtime, allocation,
onHandoff)`. Pass the same normalized allocation and callback to
`compiler.RecipeJobWorkerOptions`, with `StageExecution: wrapped.Stage` and
`WrapTaskWorker: wrapped.WrapTaskWorker`, and
build the engine using the wrapped runtime. Register schemas using the original
runtime. The CLI performs this wiring for all execution entry points.

The compiler's registered workers automatically retain task metrics after
admission. The compiler adds accumulated totals under `c2j_metrics` in successful
job outputs. This runtime also adds snapshots to existing recipe success/failure final
chapter metadata, preserving business fields and error outcomes. Use the same wrapper for acquired,
supplied-lease and embedded runs. See [task metrics](../../GUIDE-Task-Metrics.md)
for measurement boundaries, producer reports, coverage and upgrade requirements.

The recipe reconstructs requirements from its input and retained activity results
on every invocation. It never restores execution state from client payload.
The compiler stages effective requirements before each task call. Guarded task
workers check them only after JobDB decides the task is unfinished. Satisfied
requirements allow execution; a mismatch publishes the requirements and yields.
Cached tasks never request historical environments, including when recovery
starts through a stale task route.

Wrap every task worker with `WrapTaskWorker` and supply that same function in
the recipe worker options. Capability, resource, and base constraints require this guard;
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
Extensions can instead return `execution_required` and exit 75 to publish demand
and yield their unfinished task. The extension checks fresh allocation facts on
its next invocation; there is no separate recovery state. An already-satisfied
request is an error. See [extension negotiation](../../GUIDE-Extension-Execution-Negotiation.md).
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

Repository defaults in `.c2/config.yaml` are read with packages from the restored
consuming workspace, immediately before tool installation. Per-property recipe/node
values override these defaults; explicit capability lists (including `[]`) disable
config capability fallback while lexical capability accumulation remains additive.
Submission and durable directives retain their stronger scalar and additive capability
semantics. Defaults are task-local and are never negotiated into durable overrides.

A late mismatch stages full effective demand and uses the existing environment-required
yield. Replay reconstructs requirements rather than treating scheduling snapshots as
state. Completed tasks skip workspace config reads and historical admission. This needs
an upgraded worker; older workers do not enforce repository execution defaults. The
runtime supplies no new provisioning or provider selection. Passthrough consumers
without an admission bridge reject constrained work before tools run.

Each unfinished attempt restores normally and recomputes defaults from that
workspace. A moved floating ref can therefore yield again with a changed demand;
there is no preparation checkpoint or ref-pinning guarantee. Ordinary pinned
revisions and accepted snapshots keep their existing restoration semantics.
Late preparation failures log config provenance, including the digest, workspace
scope and restored revision when available, without changing handoff schemas.

## Extension wait gates

Exit-76 extension waits discard the invocation before yielding its unfinished
task. The guard reads `c2j.extension_wait` in client payload before Git, tools,
configuration or process setup. Its versioned gate binds normalized prerequisites,
a fixed earliest launch time and retained absolute deadline to `InvocationID` and
`AttemptNumber`. It contains no extension progress or business inputs.

The lease wrapper atomically composes this gate, execution demand and unrelated
client JSON under the current renewable lease's payload revision. Dependencies
use a fresh binding ID for each response. Once satisfied, a separate timed
reschedule enforces the timestamp; structured dependencies are never combined
with legacy timing. Resume uses the enclosing job route and clears stale task
coordinates. A released gate remains until its bound outcome commits, protecting
its original deadline across another crash, then is removed at a later
reschedule or completion. Completed replay never reads mutable gate conditions.

Deadline extraction understands the pinned JobDB execution version 4 chapters
and `__execution_prefix_v3` declaration metadata, including continuation budgets.
Unreadable active history, unsupported gates and unexplained identity or binding
mismatches fail closed. Check this integration when upgrading JobDB's compact
history format. See the [extension contract](../../EXTENSION_OPS.md#discard-an-attempt-and-wait).
