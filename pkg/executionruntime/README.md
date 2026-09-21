# Execution-aware JobDB runtime

Wrap a persistent JobDB runtime with `executionruntime.New(runtime, allocation,
onHandoff)`. Pass the same normalized allocation and callback to
`compiler.RecipeJobWorkerOptions`, with `StageExecution: wrapped.Stage` and
`WrapTaskWorker: wrapped.WrapTaskWorker`, and
build the engine using the wrapped runtime. Register schemas using the original
runtime. The CLI performs this wiring for all execution entry points.

The adapter checks current published demand at lease admission, including
task-only leases. An incompatible lease is rescheduled with the same route and
task coordinates, without rewriting the snapshot. Recipe preflight separately
resolves and checks the pinned base when no snapshot has been published.

For recipes using `execution_needs`, recipe leases replay first: an older
published scope may belong to completed work. The compiler stages the inherited
needs before each task call, and guarded task workers check them only after
JobDB has decided the task is unfinished. Completed results bypass the worker
and do not request historical environments. This also applies when recovery
starts through a task route: its saved task may already have completed before
the executor crashed. The next genuinely unfinished task is checked instead.

If building a workset manually, wrap every task worker with `WrapTaskWorker`
and supply that same function in the recipe worker options. This is required
for scoped needs; staging alone cannot enforce a live-task boundary.

Node scopes are ephemeral. Any subsequent ordinary reschedule publishes the
staged scope if it differs from the saved snapshot, without mutating permanent
job overrides. Explicit job requirements still override recipe and node
declarations. No live scheduler updates or automatic downsizing are performed.

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
