# Goal discovery and dependency inspection

## Required APIs

The goal runtime supports individual goal reads, candidate attachment, explicit
failure, job filtering by goal, and dependency-wait reads by known wait ID.
It does not provide tenant-wide goal enumeration or discovery of waits by
consumer or producer. These are needed to operate goal-based workflows without
scanning job histories or treating a page of jobs as an authoritative goal list.

### Paginated goal listing

Provide a tenant-scoped ListGoals operation with opaque pagination tokens and
stable ordering. Support filters for state, exact goal IDs, creation/update time,
and candidate job metadata or repository membership where feasible. A candidate
filter selects goals having matching candidates; it does not imply exclusive
ownership by that repository.

Each row should expose the authoritative goal state, immutable winning job,
failure snapshot, timestamps, revision, and candidate counts, distinguishing
active candidates from terminal candidates. Include goals with no active
candidates. Counts must cover every candidate, including additional goal
memberships and archived jobs, rather than the current page of jobs. Document
visibility semantics and how deleted or unavailable candidates are represented.

### Dependency-wait discovery

Provide paginated listing by consumer job and reverse listing by target goal or
job. Return wait ID, target/condition pairs, absolute deadline, lifecycle state,
blocked reason, durable winning-job binding, and the complete failure snapshot.
Allow filtering active waits from historical completed waits. These reads must
not mutate execution history, renew leases, or resolve a new winner.

Expose the currently blocking wait ID and its target summary in job summaries
and runnable-job outcomes. Clients should be able to explain PENDING_JOBS for a
goal wait without reconstructing private execution occurrence IDs.

## Invariants and tests

- Tenant authorization applies to every read and pagination token.
- Goal state and winners come from committed goal records.
- Candidate failures leave goals open; failed goals are explicit lifecycle events.
- Empty pages and pagination must not lose matching later records.
- Additional memberships and archived candidates appear consistently.
- Concurrent candidate completion cannot change a recorded winner or consumer binding.
- Reverse indexes track pending, satisfied, failed, timed-out, and cancelled waits.
- Implement matching behavior for embedded, HTTP, and production backends.
- No implicit full-table scan should be required for each UI page or refresh.

## SDK identity consistency

Workflow SDK submission and restart methods should return the full authoritative
submission identity (tenant, goal, and job IDs), just as low-level runtime and
lease methods already return a JobHandle. Expose the current execution's full
identity directly on task/execution contexts. Job-run summaries should also
include primary and additional goal IDs.

This avoids discarding a goal ID followed by a lookup, preserves identity across
idempotent retries, and gives clients the original complete response even if a
subsequent read fails. Restart methods must also carry the caller's top-level
request ID through to the runtime unchanged.
