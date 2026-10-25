# Goal discovery and dependency inspection

Status: addressed by the supplied JobDB discovery and complete-identity contract,
2026-10-25. The original backend request is no longer blocked on missing APIs.
This document records the delivered behavior and integration acceptance criteria.
Historical reverse lookup is outside the delivered contract; consumer-based
history supplies the required historical inspection.

## Goal listing

`ListGoals` accepts a tenant, exact goal IDs, states, inclusive creation/update
time bounds, candidate selectors, page size, and an opaque pagination token.
Candidate selectors support job IDs, job types, and application metadata.
Metadata paths are relative to application metadata; values are non-null JSON
scalars. Predicates are ANDed, values within a predicate are ORed, and all
candidate predicates must match the same candidate.

Each `GoalSummary` includes committed `GoalInfo`: state, immutable winning job,
failure snapshot, revision, and timestamps. Read-time counts include:

- `CandidateCount`.
- `NonTerminalCandidateCount`: ready, running, and blocked candidates.
- `TerminalCandidateCount`.
- `UnavailableCandidateCount`: retained candidate identities unavailable as jobs.

Counts cover all primary and additional memberships, counting each job once,
including archived and soft-deleted/hidden jobs. Candidate filters select a goal
without narrowing its counts or implying repository ownership. Candidate-list
visibility flags do not change tenant-wide goal counts. Open goals with no
nonterminal candidates remain discoverable and open.

Goal `UpdatedAt` and `Revision` describe the goal record itself. Candidate
attachment, progress, and failure may change counts without changing these
fields. Consumers must not use goal revision as a cache version for statistics.

## Dependency discovery and blocked-job diagnostics

`ListDependencyWaits` supports a consumer job filter and optional lifecycle-state
filters. Omitting states returns current and retained historical waits.

A target goal or exact-job filter performs reverse lookup of currently unresolved
dependencies in pending waits only. It supports an omitted state filter or
`PENDING`; historical reverse-state requests fail. Consumer and target filters
can be combined. A resolved G edge disappears even if another target J still
blocks the same wait. Disappearance does not identify the final outcome.

Inspect the full wait by consumer or known wait ID to obtain ordered targets and
conditions, absolute deadline, lifecycle state, blocked reasons, durable
winning-job bindings, and failure snapshots. Reverse lookup is not a historical
index of every consumer that has ever depended on a target.

Reads do not write history, renew leases, elect winners, or settle waits.
Elapsed deadlines are projected as `TIMED_OUT`, even before the scheduler updates
the durable row. Reverse queries exclude those waits. Previously recorded
bindings and failure snapshots remain unchanged.

`JobInfo`, `JobSummary`, `JobRunSummary`, and `workflow.JobRunOutcome` expose
`BlockingWait` when a job is currently blocked on structured dependencies. Its
`WaitID` and dependency summaries explain `PENDING_JOBS` without reconstructing
private execution occurrence IDs.

## Pagination, transport, and authority

Both APIs return `NextPageToken`. Tokens bind tenant and filters; subsequent
pages must preserve them, though page size may change. Default page size is 100,
with a maximum of 200. Goals sort by goal ID; waits sort by consumer job ID and
wait ID. Each page is a live read, not part of a frozen traversal snapshot.
Filtering before pagination prevents expired entries from hiding later matches.

Read-only HTTP queries accept JSON bodies at:

```text
POST /v1/tenants/{tenantId}/goals/query
POST /v1/tenants/{tenantId}/dependency-waits/query
```

The path tenant is authoritative and deployment authorization applies to every
request. A cursor is not an authorization credential. These are tenant inspection
APIs, not additional delegated-worker permissions.

JobDB CLI equivalents are `jobdb goals list` and `jobdb goals waits`, with
consumer-job or target-goal selection for waits. Commands emit one JSON page.
Client applications still need their own integration with these APIs.

## Complete SDK identities

SDK engine, execution-context, and task-context `SubmitJob` methods now return
`jobdb.JobHandle`, containing `JobKey` and `GoalID`, directly from the submission
response. Callers project `handle.JobKey` where an exact-job API needs a key;
there is no follow-up lookup to recover the goal.

`SubmitRestartJob` accepts the full `SubmitRestartJobRequest` and returns a
handle. The caller's top-level `RequestID` reaches the runtime unchanged.
`ExecutionContext.Job()` and `TaskContext.Job()` expose current tenant, primary
goal, and job identity in live execution and replay. Manually constructed task
contexts know only their supplied job key. `JobRunSummary` also carries `GoalID`
and `OtherGoalIDs`.

Wrappers must forward both discovery methods on the expanded `GoalRuntime` and
adopt the new submission/restart signatures. Identity conversion must preserve
the complete authoritative response, including on idempotent retries.

## Integration acceptance criteria

- Tenant and filter changes reject reused cursors; page-size changes are allowed.
- Goal filters and counts include additional memberships and archived/hidden
  jobs, count duplicates once, and keep unavailable identities distinct.
- Goal counts refresh despite an unchanged goal revision or update time.
- Consumer queries retain historical waits and their committed bindings.
- Reverse queries contain only unresolved pending edges and exclude expired
  waits, including before scheduler persistence catches up.
- Resolving one target removes only that reverse edge while the complete wait
  remains available by consumer or ID.
- Reads never mutate history or change recorded winners and failure snapshots.
- Blocked-job views use the supplied wait ID and dependency summaries.
- Submission and restart wrappers preserve handles and caller request IDs
  without additional identity reads.
- Embedded, HTTP, and production backend adapters implement the same contract;
  indexes support queries without application-side job/history scans.
