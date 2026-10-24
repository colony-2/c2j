# Goal-oriented execution in c2j and Cortex

Status: implementation outline, 2026-10-24. Core goal execution is implemented;
tenant-wide goal enumeration and reverse waiter discovery need additional JobDB
APIs. The execution contract and current interfaces are documented in
C2J_GOALS_GUIDE.md.

Use goals as the normal dependency for obtaining successful child results. Keep
jobs as the identity of concrete executions, histories, artifacts, and actions.
Expose both identities throughout c2j, and make goals and all their candidate
jobs visible in Cortex as part of the rollout.

This is a clean cutover. Old jobs will be deleted; only jobs submitted under the
new contract need to run, restart, recover, or replay. Change public APIs and
recipe inputs directly, and update their callers together. No compatibility
adapters, old-history support, dual semantics, or data migration are required.

Implementation uses JobDB v0.0.53. Cortex now includes goal lookup, candidate
pagination, winner and failure details, goal navigation from jobs, lifecycle
actions, and explicit restart goal selection. Tenant-wide goal enumeration and
reverse waiter discovery remain follow-ups pending JobDB API support.

## 1. Semantics and recommended defaults

| Concept | Meaning |
| --- | --- |
| Job | One execution, with its own status, history, output, artifacts, and primary goal. |
| Goal | A stable request for a successful result; multiple jobs can be candidates. |
| Goal success | The first committed successful candidate wins permanently. |
| Open goal | May have running candidates, or no active candidates and need another attempt. |
| Goal failure | An explicit, permanent lifecycle decision, separate from candidate failures. |
| Consumer binding | JobDB's durable record of which candidate satisfied a particular dependency. |

Recommended defaults for the new execution contract:

- Child result waits use goal success. Exact-job waits remain an explicit choice.
- Restarting to repair an open goal continues that goal. Starting independent
  work is an explicit new-goal choice.
- Failed, cancelled, or exhausted candidates never automatically fail their goal.
- Wait timeouts and consumer cancellation stop the consumer's wait; they do not
  fail the goal or cancel producers. Existing enclosing deadlines still apply.
- Goal success is JobDB job success, not an interpretation of output fields such
  as `ok`. Existing application-level validation still runs after collection.

Example: parent P starts job A with goal G. A fails; P remains waiting on G.
An operator restarts A as B, sharing G. B succeeds. P receives B's output and
artifacts, while retaining A as the originally submitted child. Cortex shows
G as successful, A as failed, B as the winner, and P's binding to B.

## 2. Preserve both identities throughout submission

Define one shared public identity type, provisionally named `JobRef`, containing
all three identifiers:

```go
type JobRef struct {
    TenantID string `json:"tenant_id"`
    GoalID   string `json:"goal_id"`
    JobID    string `json:"job_id"`
}
```

`GoalID` is the job's primary goal. Prefer defining this type in JobDB and using
it throughout c2j. Submission and restart methods return the complete reference;
multi-submit methods return an ordered list of references. Construct it directly
from the authoritative submission response, which already contains the job key
and goal ID. No follow-up `GetJob` call, enrichment step, or lookup adapter.

Update these paths together:

- JobDB workflow SDK submission/restart methods that currently return `JobKey`.
- Public submit helpers in `pkg/recipejob`, `pkg/starter`, and `pkg/jobdbschema`.
- `workflowctl.StartJob`, task-backed submission, and recipe child launchers.
- Restricted submission in `pkg/permissions` and `pkg/executionruntime`, which
  currently reduces the returned handle to its job key.
- Child broker replies and recording, CLI submit JSON, invocation-scoped child
  discovery, multi-child results, and child groups.
- Job listing and inspection, including primary `goal_id` and `other_goal_ids`.

Return and persist the same reference through every layer. A helper may project
it to a `JobKey` for exact-job APIs without I/O, but submission paths must not
throw away goal identity. Do not derive a goal ID from a job ID or generate a
replacement ID in c2j. Require all three identifiers on successful submission.

Keep deterministic child-job identity and recovery behavior. An idempotent retry
returns the original complete reference, including its original goal. Persist
that reference in the durable child-submission result before waiting on it.

## 3. Environment, child submission, and automatic task output

Extend the existing automatic `jobs` metadata rather than requiring each op to
manually copy its submission identities into application output:

| Existing surface | Addition |
| --- | --- |
| `sequence.<node>.jobs.items[]` and equivalent state/run records | `goal_id` alongside `job_id`; retain tenant, recipe, and invocation provenance. |
| `jobs.job_ids` | `jobs.goal_ids` as an ordered convenience projection; use `items` for identity pairs. |
| Single-child submission output | `goal_id` alongside existing `job_id`. |
| Multi-child and child-group output | Both IDs per child, plus goal ID projections where job ID lists already exist. |
| `context.workflow.job_id` | `context.workflow.goal_id` for the current execution's primary goal. |
| Protected process context | `C2J_CURRENT_GOAL_ID` alongside the existing job ID environment value. |

Use the complete `JobRef` in each `jobs.items` record, alongside recipe and
invocation metadata. Derive `job_ids` and `goal_ids` from those same ordered
records, so index positions correspond. A goal can appear more than once when
multiple submitted jobs share it. Deduplicate repeated observations by tenant
and job ID, never by goal ID; conflicting goal IDs for one job are an error.
Return empty lists when the invocation submitted no children.

For example, the automatic metadata for a task that submitted two children is:

```json
{
  "jobs": {
    "job_ids": ["A", "B"],
    "goal_ids": ["G1", "G2"],
    "items": [
      {"tenant_id": "team", "job_id": "A", "goal_id": "G1", "recipe": "review"},
      {"tenant_id": "team", "job_id": "B", "goal_id": "G2", "recipe": "test"}
    ]
  }
}
```

Recipes can use `sequence.launch.jobs.goal_ids` for a goal join, or
`sequence.launch.jobs.items[0].goal_id` for one child. This metadata is available
for command/extension tasks that submit through the broker as well as native
recipe ops; authors do not need to parse command stdout or echo IDs themselves.

### Environment and broker flow

1. Build the current execution context from its authoritative `JobRef`. Export
   `C2J_CURRENT_TENANT_ID`, `C2J_CURRENT_JOB_ID`, and `C2J_CURRENT_GOAL_ID` together
   through the existing protected environment path. Update environment parsing,
   validation, `Current`, `Parent`, and parent-context propagation as one change.
   A partial identity is invalid. Recipe-provided environment values cannot
   override the driver-supplied identity.
2. Keep the existing `C2J_CHILD_JOB_ENDPOINT`, `C2J_CHILD_JOB_TOKEN`, and
   `C2J_CHILD_JOB_SESSION_ID` transport. A subprocess calling `c2j submit` uses
   that broker, which stamps the authoritative parent identity and invocation
   metadata. Carry the parent's goal for provenance; normal child submission
   still creates the child's own goal and does not inherit the parent's goal.
3. Return the child's complete `JobRef` in the broker/CLI response and record it
   in `StartedJobs()` immediately after successful submission. Use the same
   result for host-side local submission and subprocess HTTP submission. Do not
   perform a goal lookup after receiving a successful submission response.
4. Populate automatic output from broker records and the existing invocation-
   scoped child discovery path. Discovery must return goal IDs in job summaries
   and traverse all pages, including archived children. Keep its parent-job and
   invocation filters; a shared parent goal does not make another candidate's
   children part of this task's submitted-child list.
5. Merge records across op steps, retries, and discovery using complete
   references. Preserve them in durable task output, failed-task output, and
   sequence/state/run template metadata. Remove job-ID-only merge fallbacks;
   never drop a goal while combining broker results with discovered children.
6. When a child itself runs, its protected environment contains its own job and
   goal IDs. Its descendants are attributed to that execution. Replacing a job
   within a shared goal changes the current job ID while retaining the goal ID.

The environment describes the current execution. The changing list of children
submitted during that execution is returned through broker responses and the
automatic `jobs` output; it is not maintained as a mutable environment variable.
A goal's other candidates belong in goal inspection, not in the list of jobs
this invocation actually submitted.

Add a separate result provenance envelope, such as
`result: {goal: G, job: B}`, to collected child results. The existing child
`job_id` remains A, the originally submitted job; it must not silently become B.
Keep this provenance outside arbitrary recipe output to avoid field collisions.
Update task envelopes, template adapters, child discovery/merging, story DTOs,
schemas, mocks, and failed-task metadata so IDs survive retries and inspection.

## 4. A shared wait contract

Use one nested `wait` mapping across child recipe ops and child groups:

- `on: goal` is the new default; `on: job` selects the exact execution.
- `condition: success` applies to goal waits and result-producing waits.
- `condition: complete` is available for exact-job completion/status waits,
  including unsuccessful terminal outcomes. Reject goal-plus-complete.

For standalone waits, accept a nested `target` with exactly one `goal` or `job`.
A job target with goal mode resolves that job's primary goal once and records the
resolved target durably. An exact-job wait requires a job target. IDs are opaque;
never infer their kind from their spelling. Replace standalone `job_id`/`job_ids`
wait inputs with this target model and update recipes, schemas, and callers.
Keep job IDs in submission output and exact-execution APIs.

| Operation | Proposed behavior under the new contract |
| --- | --- |
| `recipe.run_and_get_result` | Submit, persist both IDs, await the goal, collect the winner. |
| `recipes.run` | Submit only; return handles containing both IDs. |
| `recipes.run_and_wait` | Default to goal success for each child; return bindings as well as submitted handles. |
| `recipe.await_result` | Support goal/job mode; default to goal and return the bound successful result. |
| `recipe.await_result_soft` | Support both modes. In goal mode, return success or explicit goal failure as data; candidate failure alone does not end the wait. Current-status inspection can return an open goal. |
| `recipe.get_result` | Remain nonblocking. An explicit job target reads that job; a goal target reads its committed winner, or reports open/failed state. |
| `child_group` | Default child joins to goal success, with a group default and per-child wait override. |

Exact-job mode remains useful for triage, evaluating failed/partial outputs,
cleanup completion, and evaluating independent competing solutions. Select
exact-job mode explicitly in recipes that need these behaviors.

Implement waits through `AwaitGoals`/`AwaitDependencies` on the live workflow or
task context. Extend `ops.JobTool` with binding-returning methods. Do not add
polling loops or keep a worker lease alive while a goal is pending. Submission
admission dependencies use the same structured target/condition model. Use
JobDB's structured dependencies exclusively for these submissions.

## 5. Winner-aware collection and child groups

Persist the returned binding before collecting results. Load output, artifacts,
Git result metadata, and any restored workspace state from `SatisfiedByJobID` in
the same tenant. Do not inspect the original child or scan for the latest
successful candidate to choose a result. Recovery and replay reuse the binding.

Validate the winner's c2j output envelope and expected result contract. A goal
can be satisfied by an independently attached job, potentially from another
recipe or repository. JobDB success does not guarantee output compatibility.
An incompatible result fails the consumer's collection/validation; it does not
change the winner or cause a search for another candidate.

Child-group changes:

- Persist submitted job, primary goal, effective wait policy, winner binding,
  wait outcome, and result provenance per child; preserve declaration order.
- Keep required/optional failure aggregation. An explicit failed required goal
  blocks success; an optional failed goal becomes a warning. A failed candidate
  under an open goal is progress information, not a completed group failure.
- `required: false` controls aggregation, not an implicit deadline or permission
  to ignore an open goal. Use exact-job mode for optional work where a terminal
  candidate is enough; use an explicit wait deadline where bounded waiting is
  needed. Do not silently invent an optional-child timeout.
- JobDB's combined dependency join closes when one dependency fails. Preserve
  collect-all behavior through independent durable child joins with recorded
  outcomes; simply catching one failed `AwaitGoals` call does not mean the other
  goals are ready. Keep any fail-fast variant an explicit policy.
- Extend aggregate artifacts and review packs with original job, goal, winner,
  and goal-failure details. Partial output from a failed candidate remains an
  exact-job inspection feature, not the successful output of the goal.

## 6. Restart while carrying the goal forward

Extend the public restart request, starter/schema adapters, story service,
Cortex request types, and CLI restart entry points with an explicit goal mode:
`goal: {mode: continue}` or `goal: {mode: new}`. Map continue to JobDB
`SubmitRestartJob.ShareGoal = true`.

Default restart to continue the source's goal. A closed source goal rejects a
new shared candidate; report that conflict and require an explicit new-goal
action. Never silently switch modes.

- Carry the source's primary goal only. Additional goal associations are not
  inherited; attaching a restart to those goals is a separate operator action.
- Preserve current logical scope selection and context patch behavior. Goal
  sharing does not replace restart-boundary validation or replay semantics.
- Return the destination `JobRef`, with the source-job relationship.
- Carry top-level `SubmitRestartJobRequest.RequestID` through the entire public
  API. Persist it before submitting a generated-ID shared restart. Cortex keeps
  the same request identity across network retries/reloads; CLI callers can
  supply a reusable request ID. A new intended attempt uses a new ID.
- Idempotent retries must reach JobDB with the same ID and payload even if the
  destination has progressed or the goal has since closed. A fresh preflight
  goal-state check must not defeat reconciliation of an uncertain prior success.
- Replayed retained child-submission records keep their original child handles
  and bindings. Re-executed child submissions follow existing restart identity
  rules; sharing the parent's goal does not implicitly share every child's goal.
- The original job and other candidates may continue running. Continuing a goal
  neither cancels competitors nor guarantees the restart will be the winner.

## 7. Goal inspection and operator actions

Add public goal inspection/candidate services and corresponding `c2j goals`
commands for get, candidates, attach candidate, and fail. Add goal filtering to
job lists through `ListJobsRequest.GoalIDs`, including primary and additional
associations. Goal candidate views must include terminal and archived jobs;
ordinary runnable-job list defaults are inappropriate here.

Keep two distinct solution patterns visible:

- Accept any success: attach running candidates; the first committed success wins.
- Evaluate before accepting: run independent goals, inspect their exact jobs,
  and attach only the chosen result to the still-open target goal.

Existing candidates remain eligible during evaluation. There is no exclusive
selection, winner replacement, reopen, or automatic loser cancellation.

Candidate attachment and explicit goal failure use tenant lifecycle authority.
The existing `perms.jobdb.jobs.submit` queue permission does not authorize these
mutations. Waiting and result collection stay driver-mediated; do not broaden
operation tokens merely to inspect goals. Future automated repair/adoption ops
need explicit authority and durable mutation records, not side effects during
recipe replay or story reads.

Preserve the complete JobDB goal-failure object, including message, timestamp,
and terminal/nonterminal candidate snapshots. Show cycle rejection as an
invalid dependency, not a transient failure to retry unchanged.

## 8. Public read model for Cortex

Cortex should consume supported c2j Go APIs, continuing its current use of
`pkg/recipejob` and `pkg/story`; extend `pkg/joblist` where applicable. It should
not decode c2j payloads or import CLI internals to recover goal semantics.

Provide typed, paginated views for:

- A goal's state, primary provenance, winner or failure, and candidate counts.
- Every candidate, its primary/additional membership, job status, restart
  relationship, repository/recipe, timestamps, and result/story availability.
- A consumer's dependency waits: target, condition, absolute deadline, diagnostic,
  recorded failure, and durable winning job binding.
- Job-to-goal navigation, goal filters, and jobs belonging to a goal across cells.

Goal membership is tenant-scoped and many-to-many. A repository filter is a
view of candidate membership, not proof that a goal belongs exclusively to one
repository. Never build an authoritative goal status or complete candidate count
by grouping only the current page of job results. Handle hidden/unavailable jobs
explicitly without changing a recorded winner or misreporting an empty goal.

The supplied guide documents goal lookup, candidates, job goal filters, and
individual dependency-wait inspection. It does not establish a tenant-wide
paginated goal enumeration API or a reverse index of waiting consumers. Confirm
those interfaces in the goal-enabled JobDB revision. If absent, request goal
listing/filtering and reverse waiter discovery, or specify a rebuildable indexed
read projection; do not hide full-table scans behind Cortex pagination. JobDB
remains authoritative for goal state and bindings. Basic get/candidate views can
ship first, but a complete goal browser is a required delivery item.

## 9. Cortex experience

Build on the existing Go handlers in `server/internal/cortex`, shared browser
API/types in `web/shared`, and job list/story components in `web/app`.

- Add persistent Goals navigation and a tenant-scoped goal list. Show open,
  successful, and failed goals; distinguish open goals awaiting a candidate from
  those with active attempts. Allow filtering by state, repository/cell, and ID.
- Add a goal detail page with all paginated candidate jobs, the committed winner
  or failure reason, restart relationships, and waiting consumers when indexed.
  Keep story, artifact, input, review, and cancellation actions tied to exact jobs.
- On every job list/detail/story, expose its primary goal and additional goals,
  with navigation to the goal and its other candidates. A job's failure should
  not make a still-open goal appear failed.
- On child task nodes, show originally submitted job, waited-on goal, actual
  result producer, and the consumer's recorded binding. Explain blocked states
  using `waiting_for_execution`, `awaiting_candidate`, failed, timed out, and
  cancelled diagnostics. Do not infer the winner from the selected job row.
- Extend the existing restart dialog with Continue this goal / Start a new goal,
  showing the chosen goal, existing candidates, and conflict state. Preserve
  restart-context previews and patch editing. Navigate to the new job while
  retaining a prominent route back to the shared goal.
- Add operator actions to attach a candidate and permanently fail a goal with a
  reason. Explain that failure does not cancel running jobs and cannot be undone.
  Preserve stable retry identities and surface closed-goal conflicts.
- Keep goal and exact-job routes independently addressable. Refresh candidate
  progress without changing persisted winner identity or consumer history.

Deliver the basic goal detail page, all-jobs-by-goal view, and job-to-goal
navigation alongside the c2j behavior change. Operators must have those views
when default waits begin surviving failed candidates; they are not optional
follow-up polish.

## 10. Delivery and clean cutover

1. Upgrade JobDB and runtime wrappers, including `GoalRuntime`, test doubles,
   dependency bindings, and execution-history decoding. Verify the deployed
   backend, including pgjobdb where used, supports the goal APIs.
2. Change submission APIs and outputs to carry `JobRef` end to end. Publish
   goal-aware c2j read APIs and implement Cortex goal/candidate views.
3. Implement goal-default waits, winner-aware collection, and shared-goal restart
   with durable request IDs. Record resolved wait targets, conditions, and
   bindings so recovery and replay of new jobs remain deterministic.
4. Add candidate attachment, explicit failure, and the complete Cortex goal
   browser/operator experience. Update recipes that need exact-job waits to
   select that mode explicitly.
5. Update CLI help, recipe schemas/examples, authoring skills, tests, and Cortex
   dependency pins for the single new contract. Replace obsolete fixtures;
   do not retain tests or adapters for the retired contract.
6. At cutover, stop old submitters/workers, delete old jobs, deploy the coordinated
   JobDB/c2j/Cortex versions, and start submitting new jobs. Goal-default waits,
   shared-goal restart, and Cortex goal/candidate views ship together.

Do not backfill old jobs, replay old histories, preserve old restart defaults,
or provide a legacy operating mode. Exact-job waits remain a supported feature
of the new contract. Require a goal-capable backend; unsupported deployments
must fail clearly rather than silently change dependency semantics.

## 11. Acceptance scenarios

- All submit paths, including restricted leases and broker-launched children,
  return the complete tenant/goal/job reference without an identity lookup; retry
  after a lost response returns the same reference and creates no duplicate.
- Command/extension subprocess submission through the protected environment
  and broker exposes both IDs in CLI JSON and automatic `jobs` output, without
  stdout parsing. Native and local-broker submission produce the same shape.
- Nested children receive their own current job/goal environment; shared-goal
  replacement preserves the goal without misattributing another job's children.
- Broker/discovery merging, pagination, archived children, op failure, retries,
  and replay preserve complete references and matching job/goal projections;
  multiple jobs sharing one goal remain separate items.
- A failed original plus a successful shared restart unblocks the parent with
  the replacement's output, artifacts, and Git/workspace provenance.
- Competing successes produce one immutable winner; independent evaluated
  results can be attached without changing their own primary goal.
- Recovery before/after binding persistence and output collection returns the
  same winner; replay performs no new submit, attachment, failure, or election.
- Exact-job complete/success, goal success, soft inspection, optional/required
  groups, and nonblocking reads each retain their documented semantics.
- Open goals with no active candidates remain visibly pending. Explicit failure,
  consumer timeout, and cancellation are distinct and do not cancel producers.
- Shared-restart retries reuse their destination even after progress/goal closure;
  ordinary new-goal restarts and context patches still work. Extra memberships
  are not copied, and closed-goal conflicts never produce an independent fallback.
- Cortex can reach every candidate from any goal/job page, including additional
  associations, archived jobs, and cross-cell candidates, with correct pagination.
- Goal failure details, invalid cycles, unavailable results, incompatible winner
  output, unsupported servers, and unauthorized lifecycle mutations remain clear.

Run the execution cases against embedded and remote JobDB, plus the deployed
backend implementation; add Cortex API/UI coverage for navigation, restart,
blocked diagnostics, immutable winners, and all candidate pages.
