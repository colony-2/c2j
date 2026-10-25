# Cortex: goal-oriented execution and operator views

## Objective

Make goals and their candidate jobs visible throughout Cortex. A user waiting
for a result should be able to follow the goal across failed attempts and
restarts, while retaining access to each execution's history and artifacts.

This is a clean cutover. Old jobs will be deleted. Support only jobs created
under the new contract: no data migration, backfill, old-history support,
compatibility adapters, or legacy operating mode.

## Execution contract

| Identity | Meaning |
| --- | --- |
| Tenant | Authorization and identity boundary for jobs and goals. |
| Job | One execution, with its own status, history, inputs, and artifacts. |
| Goal | A request for a successful result, potentially served by several jobs. |
| Winner | The first candidate whose success JobDB commits for the goal. |
| Consumer binding | JobDB's durable record of the job that satisfied a wait. |

A goal is `OPEN`, `SUCCEEDED`, or `FAILED`. A failed, cancelled, or exhausted
candidate leaves its goal open. Explicit goal failure is permanent and does
not cancel candidate jobs. Success fixes the winner permanently; there is no
winner replacement, reopening, or automatic cancellation of other candidates.

Each job has a primary goal and can also be a candidate for additional goals.
Membership is tenant-scoped and can span repositories. A repository filter is
a view of candidate membership, not exclusive ownership of a goal.

Example: parent P submits A for goal G. A fails, but P continues waiting on G.
An operator restarts A as B, continuing G. B succeeds, and P receives B's result.
Cortex must show A as failed, B as the winner, G as successful, and P's recorded
binding to B. These are distinct facts.

## Required views

### Goal details and candidate jobs

Provide independently addressable tenant-scoped routes:

```text
/project/:tenantId/goals
/project/:tenantId/goals/:goalId
/project/:tenantId/jobs/:jobId/story
```

The goal detail page must show:

- Authoritative state, timestamps, and the committed winning job, if any.
- The complete failure record when explicitly failed: reason, timestamp, and
  snapshots of terminal and nonterminal candidate IDs at failure time.
- Every candidate through backend pagination, including primary and additional
  membership, all job types, and archived or hidden jobs visible to the caller.
- Each candidate's execution status, completion outcome, repository/recipe,
  restart relationship where available, and navigation to its job story.
- Explicit handling of unavailable candidate details without erasing a known
  winner or pretending the goal has no candidates.

Do not infer goal state, a winner, or complete candidate counts by grouping one
page of jobs. Failure snapshots describe the failure decision time; live job
statuses may subsequently change.

Show primary and additional goal links in job lists and stories. Keep artifacts,
input requests, reviews, cancellation, and execution history attached to exact
jobs. Child execution details should retain the original submitted job, waited-on
goal, and actual result producer rather than replacing one ID with another.

### Goal browsing and waiting consumers

The intended Goals landing page is a paginated tenant-wide browser, with state,
ID, and repository/cell filters. It must include open goals with no active
candidates, so operators can find work that needs another attempt.

Also show consumers waiting on a goal and, within a consumer's job story, its
dependency target, condition, deadline, wait state, failure, and recorded winner.
An open goal with no active candidates is awaiting another candidate, not failed.

JobDB now provides `ListGoals` and `ListDependencyWaits`. Implement these views
through the expanded c2j public read services; they are no longer blocked on
missing JobDB discovery APIs. The original basic-view patch does not integrate
them. Do not hide full job/history scans behind a paginated UI or present a
partial index as complete.

Use `ListGoals` for the landing page. Each summary contains committed goal state
and read-time counts: `CandidateCount`, `NonTerminalCandidateCount`,
`TerminalCandidateCount`, and `UnavailableCandidateCount`. Counts cover all
primary and additional memberships, count each job once, and include archived
and hidden jobs. Missing retained candidate identities count as unavailable,
not nonterminal. Candidate-list visibility flags do not narrow these counts.

Nonterminal includes ready, running, and blocked jobs; it is not a count of
currently executing jobs. Label it accordingly. An open goal with no nonterminal
candidates needs attention but must remain open. Do not invent a single active
candidate slot.

Goal filters include IDs, states, inclusive creation/update bounds, and candidate
job IDs, types, and metadata predicates. Use c2j's actual application metadata
keys, such as `repo` and `cell_name`. All candidate predicates must match the
same candidate; predicates are ANDed and values within a predicate are ORed.
A matching candidate selects the goal, while counts still cover the whole goal.

Goal revision and update time version the goal record, not candidate statistics.
Attachment, progress, or failure may change counts without changing either
field. Refresh counts and candidate pages independently of goal-record changes.

Use `ListDependencyWaits` in two distinct ways:

- Filter by `ConsumerJobID` to inspect current and retained historical waits.
  Omit `States` to include all retained lifecycle states.
- Filter by `Target` to find consumers currently blocked on that goal or exact
  job. Reverse lookup includes only unresolved edges in pending waits. It has
  no historical reverse lookup; omit the state filter or request only `PENDING`.

If a consumer waits on both G and J, satisfying G removes that reverse edge even
if J still blocks the overall wait. Preserve access to the complete wait through
its consumer or known wait ID. Do not label the reverse result as all past
consumers or treat disappearance as proof of successful completion.

Reads project elapsed deadlines as `TIMED_OUT` without mutating history or
waiting for scheduler persistence. Expired waits disappear from reverse lookup;
recorded bindings and failure snapshots remain unchanged. Use `BlockingWait`
on job info, summaries, and run outcomes to explain structured `PENDING_JOBS`
states and open the recorded `WaitID`; do not reconstruct occurrence IDs.

Both discovery APIs use opaque, query-bound `NextPageToken` cursors. Continue
with the same tenant and filters; reset the cursor when either changes. Page
size defaults to 100 and is capped at 200. Goals sort by goal ID; waits sort by
consumer job ID and wait ID. Pages are live reads, not a frozen snapshot across
concurrent changes. Do not claim snapshot consistency for totals or traversal.

## Submission and restart

Submission and restart return one complete reference directly from the
authoritative submission response:

```json
{"tenant_id":"team","goal_id":"G","job_id":"B"}
```

JobDB SDK engine, execution-context, and task-context submission methods now
return `jobdb.JobHandle`, including `JobKey` and `GoalID`. Restart methods accept
the full `SubmitRestartJobRequest`, preserving the caller's `RequestID`, and
also return a handle. `ExecutionContext.Job()` and `TaskContext.Job()` expose
current identity in live execution and replay. `JobRunSummary` includes primary
and additional goal IDs.

Use these native SDK identities in c2j integration. Map the handle into the c2j
public reference type and preserve all three fields through Go handlers,
browser API types, and navigation. Do not accept a job-key-only result
and perform an extra lookup to recover its goal. Fetch detailed job information
separately when a screen needs it.

The restart dialog must offer:

- **Continue this goal**, the default: add a new execution to the source job's
  primary goal.
- **Start a new goal**: independent work, including rerunning a job whose goal
  is already closed.

Example request to `POST /api/projects/team/jobs/A/restart`:

```json
{
  "request_id":"repair-attempt-1",
  "scope_id":"recorded-scope-id",
  "goal":{"mode":"continue"},
  "patch":{"job":{"repair_note":"Use the revised implementation"}}
}
```

Retain restart-context inspection and patch editing. Use the recorded scope ID
when compact history places multiple restart boundaries in one chapter. If a
chapter selector is used, it must identify an unambiguous recorded boundary;
never send both selectors or guess from a task ordinal.

Persist the request ID and payload association before sending the request. Reuse
them after a lost response or reload; a new intended attempt gets a new ID.
Successful reconciliation must still work after the accepted candidate has won
and the goal has closed. Do not reject that retry through a fresh client-side
goal-state precheck. Surface backend conflicts without silently switching to a
new goal.

Continuing a goal does not cancel the original job or other candidates and does
not guarantee the restart will win. Additional goal memberships are not inherited.
After success, navigate to the new job and retain clear navigation to its goal.

## Goal actions and API surface

Use supported c2j public services for goal reads, candidates, and lifecycle
actions. Keep browser API calls and types in the shared client package. Do not
decode private execution chapters or import CLI internals to recover semantics.

The basic HTTP surface is:

| Operation | Request |
| --- | --- |
| Read goal | `GET /api/projects/:tenant/goals/:goal` |
| Candidate page | `GET /api/projects/:tenant/goals/:goal/candidates?pageSize=100&pageToken=...` |
| Attach existing candidate | `POST /api/projects/:tenant/goals/:goal/candidates`, body `{"job_id":"B"}` |
| Permanently fail goal | `POST /api/projects/:tenant/goals/:goal/fail`, body `{"message":"No further attempts are planned"}` |
| Inspect restart context | `GET /api/projects/:tenant/jobs/:job/restart-context?scope_id=...` |

Candidate pages contain `jobs` and an optional `next_page_token`. Encode opaque
IDs and cursors safely and preserve the tenant across requests and navigation.

Add typed Cortex endpoints for goal enumeration and dependency discovery,
forwarding filters and cursors through c2j instead of filtering client-side.
Those are new Cortex routes, beyond the basic patch's routes above. The upstream
JobDB transport already exposes read-only JSON queries:

- `POST /v1/tenants/{tenantId}/goals/query`
- `POST /v1/tenants/{tenantId}/dependency-waits/query`

The path tenant is authoritative. Pagination tokens are not authorization
credentials. These are tenant inspection APIs; do not broaden delegated task
permissions to make the operator screens work.

Attaching a successful job may immediately settle the goal. Existing candidates
remain eligible; attaching a candidate is not an exclusive selection mechanism.
Failing a goal needs a reason and an explicit permanent-action confirmation.
Explain that running candidates are not cancelled.

These mutations require tenant lifecycle authority. A task's JobDB submission
permission does not grant candidate attachment or goal failure. Reads and story
rendering must not perform mutations. Report missing resources, invalid requests,
and closed-goal conflicts distinctly.

## Delivery and verification

Deploy a goal-capable JobDB backend and the goal-aware c2j interfaces together
with Cortex. The basic patch uses c2j commits `c351eaf` and `039cadb` with
JobDB v0.0.53. Updated c2j now pins JobDB main commit
`86ac742286156ce9f38305b4c8b613df70856095`
(`v0.0.54-0.20261025173555-86ac74228615`), forwards both discovery methods,
and uses native handles and current execution identity. It exposes
`recipejob.ListGoals`, `recipejob.ListDependencyWaits`, and
`recipejob.GetDependencyWait`, plus structured `BlockingWait` fields in job
lists, inspections, and stories. Adopt this updated c2j API and JobDB contract;
the original patch's dependency pin is insufficient. Update Cortex SDK callers
for handle return values and full restart requests. Keep lease enforcement,
complete-reference validation, and restart request IDs intact.

Retire old submitters/workers, delete old jobs, and submit only new jobs after
cutover. Deliver the goal browser and current waiter views as integration work,
not as requests for further JobDB APIs.

Acceptance tests must cover:

1. Submission returns a complete reference without a post-submission goal lookup.
2. A failed original remains visible after a shared-goal replacement wins; the
   consumer's displayed producer is the recorded winner.
3. Candidate pagination includes additional memberships, other job types, and
   archived jobs; changing list filters never changes authoritative goal state.
4. A continued restart preserves the goal, a new-goal restart changes it, and
   a retry after a lost response creates no duplicate, even after goal closure.
5. Compact restart scopes and context patches work through the production API;
   workspace replay remains valid when the job ID changes.
6. Explicit goal failure preserves its full snapshot and leaves running jobs
   alone. Candidate attachment and closed-goal conflicts are visible to users.
7. Tenant isolation, direct URLs, refresh, and reload preserve the selected goal
   and correct job navigation. Browser request IDs also work outside secure
   contexts where `crypto.randomUUID` is unavailable.

8. Goal listing finds open goals with zero nonterminal candidates and reports
   whole-goal counts despite repository filters or archived/hidden candidates.
9. Counts refresh when candidate progress changes without a goal revision bump.
   Filter changes discard old cursors; tenant/filter mismatches are rejected.
10. Resolving G in a wait on G and J removes only G's reverse edge. The complete
    wait and its recorded G binding remain accessible by consumer and wait ID.
11. Expired waits display `TIMED_OUT` and leave reverse results without a read
    mutation. Historical consumer waits remain inspectable; the UI does not
    offer unsupported historical reverse-state filters.

## Optional implementation reference

`CORTEX-GOALS.patch`, generated from Cortex commit `8c1f8e9` against baseline
`69e3b6f`, implements the basic goal detail/candidate views, navigation, lifecycle
actions, complete submission references, and goal-aware idempotent restart flow.
It includes API, unit, and browser coverage. It does not implement tenant-wide
goal enumeration or reverse waiter discovery. Those APIs are now available in
JobDB, but integrating them and updating the SDK dependency remains additional
work beyond the patch.

The patch is optional. All required behavior is specified above and can be
implemented independently. If applying it, review its dependency pins and adapt
it to the current Cortex tree. Its c2j pin must be available to the module
resolver; a local patch alone does not publish that dependency.
