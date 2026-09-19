# Execution-aware JobDB runtime

Wrap a persistent JobDB runtime with `executionruntime.New(runtime, allocation,
onHandoff)`. Pass the same normalized allocation and callback to
`compiler.RecipeJobWorkerOptions`, with `StageExecution: wrapped.Stage`, and
build the engine using the wrapped runtime. Register schemas using the original
runtime. The CLI performs this wiring for all execution entry points.

The adapter checks current published demand at lease admission, including
task-only leases. An incompatible lease is rescheduled with the same route and
task coordinates, without rewriting the snapshot. Recipe preflight separately
resolves and checks the pinned base when no snapshot has been published.

The compiler stages resolved demand so ordinary task/time reschedules can
publish it if needed. An explicit requirement change is a full revision-checked
snapshot on yield. The adapter preserves lease token/worker identity forwarding
needed for chapter persistence; it does not add lease matching or provisioning.

Stop or redirect an incompatible worker after `OnHandoff` to avoid a tight
reacquisition loop. The event is a handoff, not completion. Admission errors
stop keepalive for rejected leases; ordinary lease-expiry recovery applies.

Use `ops.SuspendExecution` at a successful durable activity boundary to request
a change. Read-only replay must use `ReadOnlyReplay: true` and must not publish.
See the [design](../../C2J_PORTABLE_EXECUTION_REQUIREMENTS_DESIGN.md) and
[user guide](../../GUIDE-Execution-Tracking.md) for lifecycle and failure semantics.
