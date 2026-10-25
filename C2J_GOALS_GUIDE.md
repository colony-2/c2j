# Goals and child execution

New submissions return a complete job reference:

```json
{"tenant_id":"team","goal_id":"G","job_id":"A"}
```

A job identifies one execution. A goal identifies the request for a successful
result. The first successful candidate wins permanently. Failing or cancelling
a candidate leaves its goal open. Explicitly failing a goal is permanent and
does not cancel candidate jobs.

c2j submits jobs with one attempt by default (zero automatic retries). Tasks
and recipe scopes also default to one attempt. To retry a recipe, set its
root `retry.maximum_attempts` explicitly; the count includes the initial attempt.
The recipe's root scope enforces that budget without an additional outer job
retry multiplying it. Exhausting retries fails the candidate and leaves its
goal open for another candidate or explicit goal failure.

## Waiting and collecting results

Child recipe operations and child groups wait for goal success by default.
If child A fails and a replacement B succeeds for the same goal, the parent
collects B's output and artifacts. Collection uses JobDB's recorded winner;
replay does not select a different candidate.

```yaml
sequence:
  - id: launch
    op: recipes.run
    inputs:
      recipes:
        - name: review
  - id: result
    op: recipe.await_result
    inputs:
      target:
        goal: "${{ sequence.launch.outputs.goal_ids[0] }}"
```

A standalone target contains exactly one `goal` or `job`. A job target in goal
mode resolves that job's primary goal. An exact-job wait is explicit:

```yaml
op: recipe.await_result_soft
inputs:
  target:
    job: "${{ inputs.candidate }}"
  wait:
    on: job
    condition: complete
```

Use `wait.on: job` on `recipe.run_and_get_result`, `recipes.run_and_wait`, or a
child group when the original execution itself is the dependency. Result waits
require success. Completion waits can use `condition: complete` only in job
mode. Soft exact-job waits return failed/cancelled status and available partial
results as data. Soft goal waits finish on success or explicit goal failure;
a failed candidate under an open goal remains pending.

`recipe.get_result` is nonblocking: a job target reads that execution; a goal
target reads its committed winner. `recipe.await_result_soft` also supports
`return_when: current_status` to inspect an open goal without waiting.

Use enclosing node timeouts to bound waits. A timeout or consumer cancellation
does not fail the goal or cancel its producers.

Child groups accept a group `wait` mapping and per-child overrides. Required
and optional outcomes retain their aggregation rules. An optional open goal
still waits; select exact-job completion for optional work where an unsuccessful
terminal candidate is an acceptable stopping point. Independent child joins
allow other children to complete after an explicitly failed goal.

Collected results include `result: {goal: G, job: B}`. Child-group records retain
the submitted `job_id`, `goal_id`, effective wait policy, and winning `result`.
Goal failures include their complete candidate snapshot. Arbitrary application
output remains under `outputs`.

## Process context and automatic child output

The protected environment contains `C2J_CURRENT_TENANT_ID`,
`C2J_CURRENT_JOB_ID`, and `C2J_CURRENT_GOAL_ID`. All three are required when
parsing process context. Child submissions through the broker return the full
reference and preserve the parent's goal as provenance. Children create their
own goals.

Automatic task metadata includes:

```json
{
  "jobs": {
    "job_ids": ["A", "B"],
    "goal_ids": ["G1", "G2"],
    "items": [
      {"tenant_id":"team","job_id":"A","goal_id":"G1","recipe":"review"},
      {"tenant_id":"team","job_id":"B","goal_id":"G2","recipe":"test"}
    ]
  }
}
```

This works for native operations and subprocesses using `c2j submit`. The ID
lists have corresponding positions; multiple jobs sharing one goal remain
separate records. Repeated observations are deduplicated by tenant and job,
and conflicting goal identities are errors. Empty metadata uses empty arrays.
The recipe context exposes `context.workflow.goal_id` alongside `job_id`.

## Restart

Restart continues the source's primary goal by default. Use a stable request ID
for one intended restart and reuse it for retries, including after an uncertain
response. A new attempt needs a new request ID.

```sh
c2j restart A --scope 'recorded-scope-id' --request-id repair-1
c2j restart A --scope 'recorded-scope-id' --request-id independent-1 --goal new
```

A closed goal rejects a new shared candidate. The caller must explicitly choose
a new goal; there is no automatic fallback. An idempotent retry of an already
accepted restart still returns its original reference after the goal closes.
Additional goal memberships are not inherited. Existing context patches and
restart-boundary selection remain supported by the story API.

## Inspection and lifecycle operations

```sh
c2j goals get G
c2j goals candidates G --page-size 100
c2j goals candidates G --page-token 'returned-cursor'
c2j goals attach G B
c2j goals fail G --message 'No further attempts are planned'
c2j list --goal G --json
```

Commands use the configured tenant or `--jobdb`. Candidate listing includes
primary and additional associations, all job types, and archived/hidden jobs.
Use its pagination token to reach all candidates. An ordinary job list still
applies its other filters and is not an authoritative goal state or count.

Attachment and failure require tenant lifecycle authority. A task's
`perms.jobdb.jobs.submit` permission does not grant these actions.

The public Go APIs expose `jobref.Ref`, goal reads, paginated candidates,
structured submission dependencies, goal lifecycle actions, and goal-aware
restart requests. Submission constructs references from the runtime response
without looking up the goal afterward. Task workers must use the execution
runtime guard to preserve lease-bound submission and permission enforcement.
Current identity comes directly from the native execution/task context.

## Goal and dependency discovery

c2j exposes JobDB's `ListGoals`, `ListDependencyWaits`, and known-wait lookup
through public `recipejob` services and the workflow engine wrapper. Job lists,
inspection, stories, and run diagnostics preserve structured `BlockingWait` data.

```sh
c2j goals list --state OPEN --repo https://github.com/acme/app.git --page-size 100
c2j goals list --candidate-job B
c2j goals waits --target-goal G
c2j goals waits --target-job B
c2j goals waits --consumer-job P
c2j goals wait P WAIT
```

These commands return JSON, use the configured tenant or `--jobdb`, and perform
one page/read per invocation. Discovery pages expose the native `nextPageToken`;
pass it unchanged via `--page-token` with the same filters. `goals list` also
supports `--goal`, `--job-type`, `--cell`, and RFC3339 creation/update time bounds.
Candidate repository and cell filters use metadata keys `repo` and `cell_name`.
Use `--state` to filter consumer history; reverse target queries accept only
`PENDING`. Cortex still needs to connect its goal browser and waiter views to
these discovery services.

`ListGoals` returns authoritative goal records and read-time counts across all
primary/additional candidates, including archived and hidden jobs. Nonterminal
includes ready, running, and blocked jobs. Counts can change without a goal
revision or update-time change. No page of jobs is a complete goal index.

`ListDependencyWaits` by consumer includes current and retained historical waits.
Reverse lookup by target includes only unresolved dependencies in pending waits;
it does not provide historical reverse lookup. Read-time deadline expiration
projects `TIMED_OUT` and removes the edge from reverse results without mutating
history. Inspect the consumer or known wait ID for full history and bindings.
Both listings have query-bound cursors, live-read pagination, and a page-size
limit of 200.

The new SDK returns complete `JobHandle` values from submission and restart,
accepts full restart requests with request IDs, and exposes current identity via
execution/task `Job()` methods. c2j uses those native identities
and `JobRunSummary` goal fields while preserving its complete reference shape
and lease restrictions. Current goal identity is not copied from a cached task
result, so a restart with a new goal gets its own identity during execution and replay.

## Deployment

c2j pins JobDB main commit `86ac742286156ce9f38305b4c8b613df70856095`
(`v0.0.54-0.20261025173555-86ac74228615`). Deploy a backend with the same
contract and update SDK callers together: submissions return complete handles,
and restart submission accepts the full request including its request ID.
This is a clean cutover: old histories and old standalone `job_id` wait
inputs are not supported. Update failure-oriented recipes to select exact-job
waits where appropriate before submitting new jobs.
