# Follow-up request: publish client state without ending execution ownership

## Request

Please provide or identify a supported way for an execution owner to durably
publish client-owned JSON, visible in job reads and paginated listings, while
retaining its current execution ownership and continuing the invocation.

This is the remaining non-suspending publication requirement from
[the client-payload request](JOBDB_CLIENT_PAYLOAD_FEATURE_REQUEST.md), section
"5. Atomicity and authorization". The separate client payload, revision checks,
preservation, and explicit yield API already address the other core ownership
and handoff needs. No resource-aware routing or lease matching is requested.

## Verified current contract

Reviewed JobDB `v0.0.19-0.20260919034646-71b6668a65db`, commit
`71b6668a65dbcc08ae46119412c559f54d8a4b32`. Upstream `main` and `HEAD` still point
to that commit when checked on 2026-09-19.

- `ExecutionLease` exposes client payload and revision, keepalive, completion,
  reschedule, and child/restart submission. It has no client-state publication
  operation that retains execution ownership.
- Workflow `JobContext` exposes the payload snapshot and revision, plus `Yield`.
  A successful yield reschedules and stops the invocation.
- Client updates can accompany submission, rescheduling, job completion, and
  guarded external task completion. None supplies the requested behavior for
  an already-running job that should continue normally.
- Ordinary chapter writes deliberately do not update client state or scheduler
  rows. Immutable submission metadata cannot publish facts learned later.

These findings agree with [the client-payload migration guide](MIGRATION-CLIENT-PAYLOAD.md).
The [typed-route migration](MIGRATION-TYPED-ROUTES.md) does not change that
publication contract.

## Why this is needed

A client can discover authoritative information only after execution starts.
For example, an executor resolves a deferred input and learns the current
requirements for continuing its work. If its environment already satisfies
those requirements, it should continue while making the resolved information
visible to observers and later recovery attempts.

The same need applies to publishing attempt-allocation observations and other
client-defined state while work remains active. Submission-time hints are not
substitutes for facts learned during an execution attempt.

Required sequence:

1. An executor acquires ownership and durably resolves an input.
2. It publishes newly resolved client state under that ownership.
3. Job reads and listings observe the accepted state while execution is active.
4. The same invocation continues without releasing/reacquiring ownership or
   manufacturing a task handoff.
5. If it later loses ownership or crashes, accepted state remains available to
   observers and the next authorized owner.

This request is distinct from an actual handoff: when work must move to another
executor, the existing atomic client-update-and-yield contract is appropriate.

## Required behavior

- Only an authorized current execution owner may publish this state. Expired,
  cancelled, finalized, or superseded ownership cannot overwrite it or revive
  execution.
- Successful publication does not complete the job, yield, change the route,
  alter waits/dependencies, or reset execution/retry policy. The same owner can
  continue ordinary execution and chapter writes.
- Readers can obtain accepted state through normal job reads and paginated
  listings without replaying the job, reading recipe/input artifacts, or
  scanning task history for each listed job.
- Publication is durable before success is acknowledged. Client-defined state
  and its revision are observed consistently, with stale-revision conflicts
  surfaced to the caller.
- Subsequent operations under the same ownership can use the accepted revision.
  Context accessors and later yield/completion updates must not unintentionally
  use the pre-publication snapshot and overwrite newer accepted state.
- A conflict or ambiguous/lost response can be reconciled through authoritative
  reads. Callers must not blindly retry an old update against a newer revision.
- Ordinary waits, task transitions, completion, archive movement, and recovery
  preserve published state when no explicit client update is requested.
- The value remains client-owned opaque JSON. Object member ordering may change;
  JSON meaning, including exact numbers, must be preserved. Internal framework
  fields are not exposed through or merged into this value.
- Workflow clients can perform this explicit publication without bypassing
  lease fencing or relying on a concrete backend. Read-only replay cannot
  publish state.
- The contract works consistently through direct and remote runtimes and the
  supported persistent backends.

No particular method name, database field/table, internal payload layout, or
transaction implementation is prescribed. An equivalent lease-fenced read
projection is acceptable if it provides these publication and read guarantees.
This does not request that ordinary task/chapter writes implicitly flush client
state or touch scheduler rows.

## Acceptance scenarios

1. Publish state at revision N under a live lease. A separate reader sees the
   new value/revision in both job reads and listings before execution ends.
   The original invocation can write its next chapter with the same ownership.
2. Publish again or yield explicitly using the newly accepted revision. Unknown
   client-owned fields survive, and the earlier snapshot is not restored.
3. Repeat with an outdated revision: the update conflicts and changes nothing.
4. Race publication against cancellation, expiry/replacement, and finalization:
   the losing owner cannot overwrite the accepted state or resurrect work.
5. Lose the publication response, then read authoritative state and reconcile
   without duplicating an accepted logical change or discarding newer state.
6. Crash immediately after acknowledged publication. The next owner and
   listings retain the accepted state even though no yield or completion was
   performed by the old invocation.
7. Perform read-only replay: it cannot mutate the payload/projection.

## Alternatives and requested guidance

Forced yield followed by reacquisition changes execution scheduling solely to
publish a read view. Waiting until completion or a natural yield leaves active
jobs with stale/unresolved state. Per-job history inspection changes the listing
contract and query cost. A process-local cache cannot serve remote observers or
recover durably after executor loss.

A separate shared client-owned projection service could supply these guarantees,
but introduces its own durable storage, ownership fencing, and consistency
contract. Before adding that infrastructure, please confirm whether JobDB will
support this explicit publication need or identify an existing supported path
we have missed. This request does not presume that same-job handoff itself
requires any further JobDB changes.
