# Goals and child execution

New submissions return a complete job reference:

```json
{"tenant_id":"team","goal_id":"G","job_id":"A"}
```

A job identifies one execution. A goal identifies the request for a successful
result. The first successful candidate wins permanently. Failing or cancelling
a candidate leaves its goal open. Explicitly failing a goal is permanent and
does not cancel candidate jobs.

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
runtime guard so current identity and lease-bound submission remain available.

Tenant-wide goal enumeration and reverse waiter discovery require additional
JobDB APIs. No page of job results is treated as a complete goal index.

## Deployment

Use JobDB v0.0.53 or a newer compatible revision and the coordinated goal-aware
clients. This is a clean cutover: old histories and old standalone `job_id` wait
inputs are not supported. Update failure-oriented recipes to select exact-job
waits where appropriate before submitting new jobs.
