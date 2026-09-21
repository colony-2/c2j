# Portable execution requirements: implemented design

## Contract

A job keeps its identity, pinned recipe, durable results, and artifacts when
execution requirements change. An executor makes progress while it supports
the pending task and environment. An explicit job-wide requirement change, or
an unmet scoped task need, yields a client-payload snapshot; another invocation
resumes the same job. Satisfied node-scope changes can remain ephemeral.

An explicit `ops.SuspendExecution` change always yields, even when the current allocation is already
sufficient. There is no non-suspending publication requirement, projection
store, or new JobDB API. The existing revision-checked client-payload update on
reschedule is sufficient. The earlier publication feature request is withdrawn.

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

`pkg/execution.Demand` is the versioned full snapshot. It contains:

- Recipe resolution status, canonical pinned-recipe digest, and recipe base.
- Job override requirements and the validated effective overlay.
- A requirement revision and checkpoint hashes for accepted directives.
- The actual allocation at the last publishing handoff, when available.

Initial demand is in submission metadata and immutable start input. When the
submitter already has the recipe, it records the base and digest. Otherwise it
records unresolved demand with any explicit submission overrides. Submission
does not resolve a deferred recipe merely to fill metadata.

The latest yielded snapshot lives at `client_payload.c2j.execution` and takes
precedence over submission metadata. c2j replaces its namespace while preserving
unrelated client JSON, including large numbers and nulls. Publication uses a
reset with the observed payload revision; conflicts do not cause blind retries
of stale updates. JobDB internal state is never copied into client payload.

The requirement revision is distinct from the JobDB payload revision, which
also covers other client state. Neither counts execution attempts. The last
handoff allocation is historical, not a live lease or utilization record.
Completion need not publish a snapshot.

## Bootstrap and execution

1. The provisioner reads initial metadata or the latest yielded snapshot.
   For unresolved demand it may start its default environment, honoring any
   known overrides. It does not interpret or advance the recipe.
2. Actual allocation is supplied through arguments, environment variables, or
   Go options. The runtime adapter checks published demand before admitting
   either recipe or pending-task leases.
3. Recipe execution resolves and pins its root through the existing durable
   resolution path. It derives the base and checks it against the current
   allocation before executing recipe work.
4. Sufficient allocation permits execution. Resolving a compatible recipe
   alone does not require publication or suspension.
5. Insufficient allocation yields the resolved demand and stops execution.
   The CLI emits `environment_required`; the provisioner can launch a
   compatible replacement for the same job.

Already-published incompatible demand is rescheduled without rewriting the
payload or bumping its revision. Pending tasks retain their exact typed route
and task coordinates. An insufficient environment is not a recipe failure.

Ordinary task/time reschedules also carry a staged resolved base if no snapshot
has been published yet. A later task executor therefore sees the same
constraints. This remains publication on reschedule, not an independent write.

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

Scoped recipes skip the job-wide root/lease preflight: a saved snapshot may
describe completed work, and a child's override may replace a root default.
The next live task is authoritative. Metadata marks scoped submission demand
unresolved, retaining explicit submission overrides, rather than guessing a
pending scope.

At a yield, `Demand.NodeRequirements` records the lexical overlay separately
from permanent job overrides. Ordinary task/time handoffs also publish the
staged scope when it changes. Active scope changes do not write scheduler rows;
an oversized allocation does not trigger a shrink handoff. An incompatible
live task resumes via the recipe route so completed task coordinates on the
original lease cannot pin recovery to an obsolete task.

### Explicit job-wide changes

An operation calls `ops.SuspendExecution(deps, patch)` and returns its successful
result. The helper attaches a continuation directive to the durable activity
output. It does not interrupt the function or migrate a Go stack.
Resource-dependent work belongs in a subsequent durable step, not after the
helper call in the same function.

After the result is durable and before dependent work, the compiler overlays
the directive, records a checkpoint hash, increments the requirement revision,
and yields the full snapshot. Every explicit new directive yields, including
on an already-sufficient executor. Multiple patches within one activity compose
into that activity's single directive.

Replay consumes the recorded output. Matching accepted checkpoints are skipped
without restoring older patches over newer requirements. A changed directive
at the same checkpoint is an error. Successive increases and decreases
therefore survive replay without endless yields. Changes to source recipes do
not alter an already-pinned job.

Legacy inline `execution` declarations become boundary directives in the containing job.
A nonempty declaration overlays job requirements before entering the included
body; it does not allocate another executor or automatically restore old
requirements on exit. Empty declarations are no-ops. Separately submitted
children use their own recipe and overrides, without implicit parent demand.

Explicit restarts copy only the execution namespace, including accepted
checkpoints. Unrelated client state is not implicitly inherited. Normal
resumption always retains the current snapshot.

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

`run`, `run one`, `run any`, `run loop`, and `submit --run` accept individual
`--execution-*` arguments and `C2J_EXECUTION_*` environment variables for CPU,
memory, ephemeral storage, platform, image reference, image digest, and image
ID. Arguments override corresponding environment fields independently.

Targeted runs and `run any` return promptly after an environment handoff.
`run loop` emits the event and stops to avoid reacquiring the same incompatible
job; its supervisor decides what environment to launch next. A handoff is a
successful process outcome, not job completion. Disposable standalone execution
returns `EnvironmentRequiredError`; durable resumption needs a persistent
runtime.

Embedders wrap JobDB with `pkg/executionruntime`, pass the same allocation to
`RecipeJobWorkerOptions`, and connect `StageExecution` and
`OnExecutionHandoff`, with `WrapTaskWorker: runtime.WrapTaskWorker` for every
task worker. This also applies to task-route executors. Schema
registration uses the underlying runtime. Read-only story replay disables
execution checks/publication: it reconstructs history rather than executing.

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
