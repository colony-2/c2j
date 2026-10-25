# Portable execution requirements: implemented design

## Contract

A job keeps its identity, pinned recipe, durable results, and artifacts when
execution requirements change. Before unfinished work, c2j compares the current
requirements with the actual executor. If satisfied, execution proceeds. If
unsatisfied, c2j records the effective requirements in client payload and yields.
A requirement change alone does not require suspension.

Recipe execution reconstructs requirements from original inputs and cached task
results on each invocation. The client payload is a disposable scheduling hint,
not authoritative execution state. No checkpoint map, publication service, or
additional JobDB API is required.

JobDB owns scheduling and leases; c2j owns the meaning of its client JSON.
Typed routes identify job/task work. Execution requirements are not lease
matching state. Candidate selection is advisory: the executor checks again
after acquiring a lease.

## Requirements and actual allocation

All recipe root forms accept the same optional declaration:

```yaml
id: process
execution:
  image: registry.example/runner:1.2
  platform: linux/amd64
  resources:
    cpu: "2"
    memory: 4Gi
    ephemeral-storage: 10Gi
sequence: []
```

Effective demand is the recipe base overlaid with job overrides, field by
field. Later supplied values replace earlier values, including lower resource
minima. Omission retains the prior value; empty, zero, and negative values are
invalid. There is no clearing API. Submission overrides use `--require-*` or
the corresponding Go API.

CPU uses cores/millicores. Memory and storage require SI/IEC units and integral
bytes. Normalization rejects rounding and overflow. Platform is
`os/architecture[/variant]`; an omitted variant accepts any variant of that
OS and architecture.

Actual allocation is injected separately at execution start. It describes
simultaneously usable capacity, not host totals, requested capacity, measured
usage, or a previous attempt. Missing facts remain unknown and cannot satisfy
explicit requirements. No host auto-detection is performed.

Image allocation has independent `reference`, `manifest_digest`, and
`image_id` fields. Tags match the normalized launch reference. Pinned images
require the actual OCI manifest digest; a launch reference alone is not proof.
Runtime/config IDs are diagnostic, never manifest proof. Extra memory cannot
compensate for an incompatible image or platform.

## State and ownership

`pkg/execution.Demand` version 3 contains schema version, recipe resolution
status, and effective requirements. It contains no digest, base/node overlays,
directive checkpoints, demand revision, or last allocation. Handoff events
separately include the actual allocation and mismatches for diagnosis.

`pkg/execution.Submission` adds the original job overrides to the initial hint.
It is saved in metadata and immutable job input. If the submitter has an
unscoped recipe, the hint includes its base requirements. Deferred and scoped
recipes remain unresolved, with explicit submission overrides as known hints.
Submission does not resolve deferred recipes merely to fill metadata.

The latest scheduling hint lives at `client_payload.c2j.execution` and takes
precedence over metadata for listings. c2j replaces this namespace while
preserving unrelated client JSON. Publication uses a reset with the observed
JobDB client-payload revision; conflicts do not cause blind retries. JobDB's
revision handles concurrency and does not count execution attempts.

## Bootstrap and execution

1. A provisioner reads the initial metadata or latest scheduling hint. It may
   start a default environment for unresolved demand, honoring known overrides.
2. The worker loads the pinned recipe and starts from original job overrides.
3. Replay walks retained results, applying requirement directives in order.
   Cached tasks need no environment checks or activity execution.
4. The compiler stages effective requirements for the next unfinished task.
5. A guarded task worker compares these requirements with actual allocation.
   If satisfied, it runs. Otherwise it publishes the hint and yields, reporting
   an `environment_required` event after JobDB acknowledges the handoff.

An identical incompatible hint need not be rewritten. A mismatch resumes through
the recipe route so stale task coordinates cannot determine future requirements.
Ordinary task/time reschedules also publish changed staged requirements, allowing
the next worker to be provisioned for pending work. Compatible execution and
completion do not require publication or an extra invocation.

## Dynamic requirements and replay

### Scoped recipe-node needs

`execution_needs` is additive node metadata. Its literal/templated properties
resolve through the existing template engine in each node's normal context.
Same-job contexts inherit a copy and overlay explicit fields; leaving a context
restores its parent. Child jobs have independent contexts. Full effective needs
are recipe base, then the lexical node overlay, then explicit job-wide overrides.

The compiler stages each task's effective needs ephemerally. Guarded task
workers compare actual allocation only after JobDB's completed-result lookup.
No task-input format or cache key changes are needed. Cached tasks therefore
replay without requiring their old environment, including partial operations,
repeated state occurrences, and recovery through a stale task route.

All recipes skip job-wide root/lease preflight: a saved snapshot may
describe completed work, and a child's override may replace a root default.
The next live task is authoritative. Metadata marks scoped submission demand
unresolved, retaining explicit submission overrides, rather than guessing a
pending scope.

At a yield, only the effective requirements are published. Node overlays and
job overrides stay in the compiler's reconstructed state. No automatic downsizing
occurs while the executor remains sufficient.

### Explicit job-wide changes

An operation calls `ops.SuspendExecution(deps, patch)` and returns its successful
result. The helper attaches a continuation directive to the durable activity
output. It does not interrupt the function or migrate a Go stack.
Resource-dependent work belongs in a subsequent durable step, not after the
helper call in the same function.

After the result is durable, the compiler overlays its directive in memory.
Multiple patches within one activity compose into that activity's single
directive. Only subsequent unfinished work checks allocation. A final directive
with no dependent work does not cause a needless suspension.

Replay reapplies every retained directive in order, including increases and
decreases, without requiring historical environments. Recipe source resolution
and activity results already provide the durable record; no separate payload
checkpoints are needed. Changes to source recipes do not alter a pinned job.

Legacy inline `execution` declarations become boundary directives in the containing job.
A nonempty declaration overlays job requirements before entering the included
body; it does not allocate another executor or automatically restore old
requirements on exit. Empty declarations are no-ops. Separately submitted
children use their own recipe and overrides, without implicit parent demand.

Explicit restarts initialize their payload from the original submission hint
in job metadata. They do not copy the latest execution snapshot or unrelated
client namespaces. The worker reconstructs only retained history before the
selected scope, so restarting chapter 4 cannot inherit changes from chapter 20.
Restart submission needs no recipe loading or compilation. Repeated submissions
with the same request ID use the same original hint, preserving idempotency.

## Failure semantics

Only an acknowledged yield reports a successful handoff. A rejected, stale,
cancelled, timed-out, or uncertain publication stops the invocation before
dependent work. Execution-control errors bypass recipe catch/retry behavior
that might otherwise continue after a business failure. Active timeout scopes
apply to the yield.

If reschedule commits but its response is lost, durable state remains
authoritative. A later compatible executor resumes it; the previous executor
does not continue or republish stale state. A crash before publication replays
the durable directive through a fresh lease. Malformed or unsupported state is
an error, never an unconstrained job.

## CLI and embedding

`run`, `run one`, `run with-lease`, and `submit --run` accept individual
`--execution-*` arguments and `C2J_EXECUTION_*` environment variables for CPU,
memory, ephemeral storage, platform, image reference, image digest, and image
ID. Arguments override corresponding environment fields independently.

Runs return promptly after an environment handoff. A handoff is a successful
process outcome; the job remains suspended for a compatible allocation.
Polling commands and the public standalone executor are removed. Pulse continues
to discover work and dispatch it through `run with-lease`.

Embedders wrap JobDB with `pkg/executionruntime`, pass the same allocation to
`RecipeJobWorkerOptions`, and connect `StageExecution`, with
`WrapTaskWorker: runtime.WrapTaskWorker` for every task worker. After `Run`, use
`compiler.ExecutionHandoffFromOutcome` to read the acknowledged receipt from
`JobRunOutcome.Handoff`. Metadata is submitted with `YieldRequest` before
cancellation; no post-Yield callback is needed. This also applies to task-route
executors. Schema registration uses the underlying runtime. Read-only story
replay disables execution checks/publication: it reconstructs history rather
than executing.

## Listings

For waiting jobs, ordinary and child-job JSON listings expose an `execution` view derived only
from metadata and current payload, without recipe resolution or history
replay. Status distinguishes `specified`, `unspecified`, `unresolved`,
`malformed`, and `unsupported`, with source/publication information. Waiting
states include ready, dependency/time waits, expiry, and crash recovery.
Running jobs expose `in_flight` without requirements; terminal/unknown states
expose `not_waiting`. Neither is compatible with an allocation filter, including
when unresolved jobs are allowed. Raw historical client payload is unchanged.

`--compatible-with-execution` opts into filtering using the same allocation
fields. Without opt-in, inherited environment variables do not filter jobs and
explicit allocation arguments are rejected. At least one matching fact is
required; image ID alone is insufficient. `--include-unresolved` admits
bootstrap candidates while checking known overrides, without labeling them
proven compatible. Malformed/unsupported demand is diagnostic in unfiltered
JSON and an error in filtered listings.

Filtering composes with tenant, cell, parent, status, and route filters. It
scans underlying pages until the requested number of matches is found or the
source is exhausted. Tokens resume after the last examined record without
skipping matches. Default filtered page size is 100. Candidates are not
reservations, readiness guarantees, or scheduler-level resource matches.

## Verification and scope

Integration coverage includes same-job handoffs and durable result reuse on
toy, SQLite, and remote runtimes; initial checks; ordinary task handoffs;
deferred root pinning; inline boundaries; already-sufficient explicit changes;
and lost handoff responses. Other tests cover normalization, snapshots,
restarts, CLI events/injection, filtering/pagination, timeouts, and replay
control errors.

Provisioning, resource reservation, provider-specific policy, live telemetry,
and native PostgreSQL deployment are outside this implementation. See the
[user guide](GUIDE-Execution-Tracking.md) for usage and the
[upgrade notes](JOBDB_UPGRADE_NOTES.md) for fresh-storage requirements.
