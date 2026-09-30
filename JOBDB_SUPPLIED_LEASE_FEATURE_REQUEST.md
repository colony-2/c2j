# Feature request: execute an existing JobDB lease

Status: delivered in JobDB v0.0.22. This document preserves the original request;
see [c2j integration and usage](GUIDE-Run-With-Lease.md) for the implemented APIs.

## Use case

A dispatcher acquires work from JobDB, selects an execution environment, and
passes the existing lease to a c2j worker in that environment. The worker must
run that lease rather than try to claim the job again. Claiming again cannot
succeed while the dispatcher's lease is current, and silently acquiring a new
lease later would execute under different authority than the dispatcher supplied.

c2j will expose `c2j run with-lease` alongside its existing claim-and-run
commands. It needs public JobDB library support for transporting an execution
lease, validating and renewing it, and executing it through the existing runner.

## Current gap

The c2j dependency examined is
`github.com/colony-2/jobdb v0.0.19-0.20260919034646-71b6668a65db`.

- `workflow.GetJobForRun` always calls `WorkflowRuntime.GetJobLease` to claim
  work. Its underlying execution path already accepts a lease internally, but
  there is no public entry point for executing a supplied one.
- The remote runtime constructs `ExecutionLease` values from claim responses
  internally. There is no public export/import contract for moving that
  capability to another process.
- `workerRunner.DoJob` calls `KeepAlive` and discards its returned error.
  Keepalive behavior also differs by runtime: remote and SQLite renew on a call,
  while the core runtime starts background renewal. The supplied-lease path
  needs an explicit initial renewal result and observable subsequent failures.

These gaps should be addressed in JobDB so c2j does not duplicate JobDB's
transport serialization, token handling, heartbeat, or execution lifecycle.

## Required behavior

### Transport and import

Provide a supported way to export a claimed lease and import it in another
process using a JobDB client/runtime. Include the lease capability and enough
context to identify the tenant, job, and lease. The JobDB connection target may
be supplied separately; the format must document which context is required.

The imported lease must preserve the original lease identity and authority.
Import must not acquire work, change ownership implicitly, resurrect an expired
lease, or convert invalid authority into ordinary authenticated client access.
Preserve the owning worker identity where lease mutations require it; a receiver's
process name must not silently replace it.

Resolve or validate execution metadata against the authoritative lease:
route, task-wait coordinates, run policy, schema identity, client payload, and
payload revision. The receiver must not have to infer these from private token
claims or reconstruct a snapshot from unrelated job history. If the exported
representation includes a snapshot, it must not allow edited or stale fields
to override the current lease's execution state.

Support the remote runtime used between processes, and supplied in-memory
leases for embedded/library callers. Document backend support and any transport
limitations explicitly; unsupported import must fail without claiming work.

### Validate and renew before execution

Before invoking job or task work, synchronously confirm that the supplied
lease is current and renew it. Verify the capability's tenant/job/lease binding,
expiration, and backend ownership. A cancelled, completed, expired, revoked,
superseded, malformed, or mismatched lease must not execute work.

The check must consult authoritative lease state, not only token syntax or its
embedded expiration. A failed or uncertain initial renewal must return an error
without running work. If import performs this renewal, execution must still
handle any delay between import and starting the runner safely.

Expose enough renewal timing information to schedule heartbeats safely. Short
remaining lifetimes and refreshed remote tokens must work without callers
decoding private token formats.

### Execute through the shared runner

Expose a public workflow entry point that accepts an existing `ExecutionLease`
and the usual workers/options/listener. Reuse the existing paths for:

- Durable replay, task dispatch, retries, and artifact/chapter persistence.
- Lease heartbeat and cancellation.
- Job completion, task-route handoff, time/dependency waits, and rescheduling.
- Lease-scoped child submission and restart submission.

Validate that the supplied lease is appropriate for the requested job and worker
routes. Preserve its execution snapshot and capability through wrappers, including
the current token after renewal. Allow c2j's execution-allocation admission checks
to run under the validated lease before recipe work; those checks can themselves
reschedule the lease.

A supplied-lease invocation ends when that lease completes, is rescheduled,
is lost, or execution is cancelled. It must never call acquisition APIs to obtain
replacement work, including after an input wait, unsupported route, execution
environment handoff, or lease-loss error. A later invocation may use a separately
acquired lease through the ordinary dispatcher flow.

Keep the existing claim-and-run APIs available with their current selection
behavior. Both entry points should share execution machinery rather than grow
independent runners.

### Heartbeat loss and cleanup

Heartbeat failure must be observable by the runner. On confirmed lease loss,
cancel the execution context promptly, stop dispatching further work, and return
a distinguishable lease-loss error. Do not turn that event into an application
failure completion or let a terminal job read mask this invocation's lease loss.

For a transport failure or ambiguous renewal result, fail closed: the simplest
acceptable policy is to stop execution immediately. If JobDB retries renewals,
the policy must be bounded by a known safe validity deadline and must not allow
execution beyond it. Renewal requests themselves must have bounded lifetimes.

Completion and rescheduling must stop heartbeats without a race that reports
the intentional release as lease loss. All exit paths must clean up heartbeat
goroutines. Lease-protected writes remain fenced by the backend after ownership
is lost, even if application code is slow to observe cancellation.

Return errors that support `errors.Is`/`errors.As`, reusing
`jobdb.ErrExecutionLeaseLost` where appropriate. Preserve the distinction between
lost/invalid authority and transport failures. Never include the capability in
errors, logs, progress events, or ordinary diagnostic formatting.

## Suggested public API shape

The exact API is JobDB's decision. Prefer a small additive surface:

1. An explicit export/import representation for a lease capability, with a
   documented serialization format. Exporting credentials should be intentional.
2. A runtime import operation that validates and renews the supplied authority
   and returns an ordinary `ExecutionLease` with its authoritative snapshot.
3. A workflow `RunWithLease` or `GetJobForRunWithLease` entry point that consumes
   the supplied lease and uses the existing runner. It must also work for an
   already-held lease without a serialization round trip.
4. A clear renewal contract: synchronous renewal/validation and observable
   heartbeat loss, with expiry/timing information as needed by the runner.

An optional runtime interface is acceptable if it avoids breaking existing
`WorkflowRuntime` implementations. Any required remote protocol addition belongs
to JobDB and should be versioned/documented there. c2j should consume public Go
APIs, not construct private JobDB transport requests.

## Ownership and responsibility boundaries

Passing a bearer capability does not by itself create exclusive ownership for
the receiving process. The dispatcher must stop executing or renewing that lease
once it hands responsibility to the receiver. Document this coordination
requirement; this request does not require a new atomic ownership-transfer protocol
or exactly-once guarantees for external side effects.

JobDB owns capability validation, token refresh, backend fencing, and the shared
runner lifecycle. c2j owns CLI input handling and its execution-environment checks.
The c2j command will accept credentials through an owner-only file or protected
input channel, never a literal command-line argument. It will not wait and claim
another lease after the supplied invocation ends.

No changes to recipe syntax, job scheduling policy, resource matching, or review
APIs are requested.

## Acceptance tests

Use real backend/remote integration tests as well as targeted failure injection.

| Scenario | Required result |
| --- | --- |
| Dispatcher claims; separate client imports and executes | Same lease ID; normal completion and persisted output/artifacts; zero receiver acquisition calls. |
| Already-held in-memory lease | Public workflow API executes it without export/import or reacquisition. |
| Invalid token, wrong tenant/job/lease, expired or superseded lease | Fails before job/task code or chapter writes; no fallback claim. |
| Lease expires between import and run | Pre-execution validation rejects it. |
| Edited/stale exported execution metadata | Rejected or replaced with authoritative data; never changes the lease's route or execution state. |
| Work outlasts the initial lease/token lifetime | Repeated renewal keeps the same lease current; later writes and child submissions use refreshed authority. |
| Lease revoked/lost during running work | Execution context is cancelled, later tasks do not start, and a typed loss is returned without claiming again. |
| Initial or subsequent renewal times out/fails ambiguously | No initial work, or running work stops according to the documented bounded policy. |
| Input/task handoff, future wait, dependency wait, or allocation rejection | Existing rescheduling behavior runs once; invocation exits without reacquisition. |
| Resume through a task route | Task coordinates, replay, client payload/revision, schema, and artifact references are preserved. |
| Completion/reschedule races with heartbeat | Successful release is not misreported as loss; no heartbeat leaks or post-release renewal loop. |
| Caller cancellation, initialization failure, or execution error | Heartbeats stop and existing cleanup/fencing semantics are preserved. |
| Error/log/progress output | Capability never appears, including malformed input and rejected credentials. |
| Existing claim-and-run callers | Continue to acquire and execute normally through the shared runner. |

Exercise remote clients against a persistent backend, including a receiver with
no acquisition permission, and run race tests for renewal, token refresh, and
completion/rescheduling cleanup. Instrument acquisition calls so the no-fallback
guarantee is asserted, not inferred from the final job status.
