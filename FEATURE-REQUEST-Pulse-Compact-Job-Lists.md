# Pulse: schedule from lease demand without hydrating discovery rows

## Problem and required outcome

JobDB/c2j listings are compact and no longer contain current client payloads.
Pulse must keep discovery at one `ListJobs` per page and calculate current
allocation from the payload already returned when claiming admitted work.
Adding `GetJob` to the discovery loop would recreate the removed cost higher
in the stack and is explicitly outside this request's acceptance criteria.

Audited Pulse main
[`071cf32f1ef5ae6e31644d051a85c3e6e3efe022`](https://github.com/colony-2/pulse/tree/071cf32f1ef5ae6e31644d051a85c3e6e3efe022)
on 2026-10-28. See [c2j migration](MIGRATION-C2J-Compact-Job-Lists.md).

## Audit findings

| Source | Current behavior | Impact |
| --- | --- | --- |
| `internal/c2j/embedded.go:Embedded.List` | Calls `joblist.ListExecutionJobs(..., nil)` with `AvailableRoutes`; projects `JobFromSummary.Execution` | Already one compact page call. Preserve route filtering and opaque pagination; do not add detail reads. |
| `internal/c2j/jobs.go:Job.Allocation` | Accepts only `specified`, `unspecified`, `unresolved`; requires usable demand | New waiting rows report `not_loaded`; dependency bump alone causes discovery candidates to be rejected. |
| `internal/controller/controller.go` | Calls `j.Allocation` and constructs a process for each listed row before bounded scheduling | This pre-claim dependency must be removed. Filling it with `GetJob` would simply move the enrichment loop. |
| `internal/c2j/embedded.go:Embedded.Claim` | Acquires a lease, combines retained `Execution.Initial` metadata with `lease.ClientPayload()`, and returns a refreshed execution view | Already obtains authoritative demand without a separate read. Preserve this mechanism. |
| Controller `scheduler.Job.Prepare` callback | Claims the candidate, recalculates allocation, rebuilds the process, exports the lease capability | Correct location for demand-dependent decisions. Keep failure release handling. |
| `internal/scheduler/scheduler.go:prepare` | Validates initial request/process before running `Prepare`, then validates its returned launch | The pre-prepare validation currently requires a fully formed placeholder. Relax only that requirement for deferred preparation; keep final launch validation. |
| `Scheduler.Run` | Reserves eligible candidates, performs bounded `prepare`, then submits to providers | Provider submission already follows lease preparation. Preserve this ordering; no new discovery-detail phase is needed. |

## Requested implementation

1. Upgrade c2j/JobDB together and retain compact tenant discovery using
   `AvailableRoutes`. A stored next route may differ from a due alternate;
   claiming must continue to use the supported route set and the acquired route.
2. Admit bounded candidates using identity, availability, cancellation policy,
   local cooldown/eligibility, per-tenant and batch limits. Do not require a
   resolved current demand before claim. Preserve immutable submission metadata
   (`Execution.Initial` or raw metadata) for interpreting a genuinely absent
   demand in the acquired lease.
3. Represent an admitted but unprepared candidate explicitly. Move authoritative
   `Job.Allocation` and `Process` construction into `Prepare`, after `Claim`.
   The scheduler should validate identity and invariant launch metadata before
   preparation, and validate the complete launch after preparation. Keep the
   current validation for jobs without a `Prepare` callback. Do not manufacture
   a “specified” view or pretend default allocation satisfies a compact row.
4. Keep placement/provider submission after successful preparation. Enforce the
   existing bounded claim concurrency, timeout, cooldown, capacity/batch limits,
   and release/backoff behavior. Do not claim the entire discovered page just
   to learn requirements; only claim work admitted for a launch attempt.
5. A malformed or unsupported lease demand must fail preparation without
   provider submission and release/back off appropriately. Apply configured
   defaults only through the existing loaded-demand semantics, never because a
   list payload was omitted. A changed yield between discovery and claim must
   use the lease's new demand.
6. Preserve exported capability secrecy, task coordinates on task-route release,
   omission of task coordinates on job-only alternates, and no release after
   accepted/ambiguous provider submission.

## Why not fetch or cache every payload?

An unconditional per-row `GetJob` is the same removed loop. Revision-keyed
caching reduces repeat reads but still makes cold discovery O(rows) detail
requests and duplicates demand that claiming already provides. It also does
not establish current readiness or lease ownership. This request does not need
such a cache. If future admission truly requires demand before claiming, treat
that as a separate backend design requirement for compact scheduling hints or
server-side selection, not permission to hydrate UI/discovery lists.

## Acceptance tests

- A 100–200-row discovery page performs exactly one list request, zero detail,
  history, wait, or artifact reads, and no claims until admission.
- Valid `not_loaded` candidates are admitted instead of rejected; no use of
  stale `Initial.Demand` as current demand and no unconditional default launch.
- With a per-tenant limit below the page size, claims are bounded by admitted
  launch attempts. Unselected rows incur no additional JobDB operations.
- Use different submission and lease CPU/memory/image requirements. Provider
  requests and executor environment must use the lease requirements, including
  when demand changes after discovery.
- Cover unresolved bootstrap with known overrides, absent lease demand,
  malformed/unsupported demand, claim races, expiry, cancellation, due alternate
  routes, and release after failed preparation.
- Assert provider submission happens only after successful claim and final
  validation. Retain accepted/unknown submission cleanup semantics and claim
  concurrency/time bounds.
- Update embedded/controller/scheduler tests to use compact discovery fixtures;
  do not keep full list payloads in fixtures that conceal the migration issue.

This is a rollout dependency: current `Job.Allocation` rejects `not_loaded`, so
shipping only a dependency bump would stop Pulse scheduling. The intended fix
moves an existing allocation decision to the existing claim phase, with no
per-row detail fetches introduced at any layer.
