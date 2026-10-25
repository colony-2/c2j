# Recipes: use goals for child-result dependencies

## Objective and scope

Update the recipes repository's orchestration recipes, reusable definitions,
scripts, prompts, examples, and tests to use goals when waiting for successful
child results. Keep exact job IDs for inspecting and operating on a particular
execution.

This is a clean cutover for new jobs. Old jobs will be deleted. Do not add data
migration, backfill, old input aliases, job-ID enrichment adapters, or support
for replaying old histories. Update callers and definitions to the new contract
together. The examples below describe supported c2j behavior; referenced child
recipe names are illustrative, not an inventory of this repository.

## Semantics authors must preserve

A job is one execution. A goal is a stable request for a successful result.
Each submitted child creates its own goal. If child A fails, its goal G remains
open; a replacement B sharing G can succeed and satisfy the parent's wait.
The parent must consume B's recorded output and artifacts.

The first successful candidate wins permanently. Failure, cancellation, and
retry exhaustion of a candidate do not fail its goal. Explicit goal failure
is a separate permanent decision and does not cancel producers. A consumer's
timeout or cancellation also does not fail the goal or cancel candidate jobs.

JobDB success is execution success. It does not interpret application output
such as `ok: false` as failure. Define and validate the child result contract,
including business-level acceptance, before treating a winning result as usable.
If success must mean passing a gate, the producer must enforce that gate before
completing successfully. A consumer rejecting output cannot change the winner.

## Choose the dependency intentionally

| Intent | Recipe pattern |
| --- | --- |
| Obtain a usable result, allowing repaired attempts | Goal success; default for child result waits. |
| Inspect one attempt's failure or partial output | Exact-job soft completion wait. |
| Wait for one execution to succeed specifically | Exact-job success wait. |
| Read a settled goal without blocking | `recipe.get_result` with `target.goal`. |
| Read a particular job without blocking | `recipe.get_result` with `target.job`. |
| Inspect a goal now, including while open | `recipe.await_result_soft` with `return_when: current_status`. |

Use nested `target` and `wait` mappings. Do not introduce dotted configuration
keys. Existing identity field names such as `goal_id` and `job_id` remain the
public metadata contract.

## Standard child-result patterns

`recipe.run_and_get_result` waits for goal success by default. An explicit policy
can document intent at important orchestration boundaries:

```yaml
id: review-parent
version: "1.0"
input_schema: {}
sequence:
  - id: review
    op: recipe.run_and_get_result
    inputs:
      name: review-child
      inputs: {}
      wait:
        on: goal
        condition: success
outputs:
  review: "${{ sequence.review.outputs.outputs }}"
  producer: "${{ sequence.review.outputs.result }}"
```

For separate submission and collection, pass the goal returned by submission:

```yaml
id: deferred-review
version: "1.0"
input_schema: {}
sequence:
  - id: launch
    op: recipes.run
    inputs:
      recipes:
        - name: review-child
          inputs: {}
  - id: collect
    op: recipe.await_result
    inputs:
      target:
        goal: "${{ sequence.launch.outputs.goal_ids[0] }}"
outputs:
  review: "${{ sequence.collect.outputs.outputs }}"
  producer: "${{ sequence.collect.outputs.result }}"
```

`recipes.run_and_wait` also defaults to goal success and retains submitted
references plus result bindings. Standalone `recipe.await_result`,
`recipe.await_result_soft`, and `recipe.get_result` use `target`, replacing old
top-level `job_id` inputs. A target has exactly one `goal` or `job`.

For blocking waits, `target.job` without `wait.on: job` still follows that job's
primary goal. Prefer passing `target.goal` directly when the reference is
already available. For nonblocking `recipe.get_result`, the target itself
selects a goal winner or an exact job; `wait` does not change that selection.

Goal waits and result-producing waits require `condition: success`.
`condition: complete` is for exact-job completion/status waits.

## Failure inspection and repair loops

Do not mechanically convert every wait in a repair loop to a goal wait. A
controller that must inspect a failed attempt before arranging another attempt
cannot first block until that goal succeeds.

Use this node shape when observing one attempt:

```yaml
op: recipe.await_result_soft
inputs:
  target:
    job: "${{ inputs.candidate }}"
  wait:
    on: job
    condition: complete
```

The returned status and available partial outputs/artifacts are observations of
that exact job, not a successful goal result. By contrast, soft goal waits finish
on success or explicit goal failure; a failed candidate under an open goal does
not finish the wait. Soft means terminal failure becomes data, not that waiting
ends on the first unsuccessful candidate.

A repair controller should retain `{tenant_id, goal_id, job_id}` for the request
and current attempt, inspect the exact attempt, and arrange a replacement under
the same goal. Ordinary `recipes.run` submissions create new goals; they are not
a way to repair G while keeping an existing consumer of G connected.

Shared-goal restart is currently available through the c2j CLI and public
restart APIs. Candidate attachment and explicit failure are tenant lifecycle
actions. Do not assume a built-in recipe repair/attach/fail op exists or grant
tasks broad credentials to simulate one. If unattended repair requires new ops,
request explicit authority and durable, idempotent mutation support as a separate
feature. Operator-driven repair can use the existing restart controls.

Use enclosing recipe/node timeouts to bound waits. Do not implement polling
loops or add `timeout`/`poll_interval` inputs to `recipe.await_result_soft`;
those inputs are not supported. Document what should happen operationally when
an open goal has no active candidates.

## Child groups and optional work

Child groups default to goal success and support a group policy plus per-child
overrides. For example, this node waits for a required result while allowing an
optional attempt to finish unsuccessfully:

```yaml
child_group:
  mode: run_and_get_result
  wait:
    on: goal
    condition: success
  children:
    - key: implementation
      recipe: implementation-child
      required: true
    - key: advisory
      recipe: advisory-child
      required: false
      wait:
        on: job
        condition: success
  aggregate:
    shape: children
```

For this result-producing group, the optional exact-job failure is recorded and
aggregated as optional failure; it is not a successful result. Use the standalone
soft completion pattern when partial-result inspection is the objective.

`required: false` controls aggregation, not readiness. An optional child with an
open goal still waits, including when its original job failed. Do not invent an
implicit optional timeout or silently skip that goal. Audit each optional child
to choose goal-based repair, exact-attempt completion, or an explicit enclosing
deadline according to the intended behavior.

Keep child declaration order, required/optional semantics, aggregate outputs,
and artifact behavior. Update review packs and summaries to retain original job,
goal, and winning producer. A failed original must not make a successfully
repaired child appear failed.

## Identity, process context, and automatic task output

Submission returns the complete reference directly:

```json
{"tenant_id":"team","goal_id":"G","job_id":"A"}
```

Preserve it in scripts, custom outputs, prompts, and state transitions. Do not
throw away the goal and recover it through another API call. Do not overwrite
the original job ID with the winning job ID.

Automatic metadata on a task that submits children includes:

```json
{
  "jobs": {
    "job_ids": ["A", "B"],
    "goal_ids": ["G1", "G2"],
    "items": [
      {"tenant_id":"team","job_id":"A","goal_id":"G1"},
      {"tenant_id":"team","job_id":"B","goal_id":"G2"}
    ]
  }
}
```

Use `sequence.launch.jobs.items` for complete references or
`sequence.launch.jobs.goal_ids` for goal joins; equivalent state/run metadata
is available in its scope. These fields are automatic task metadata, distinct
from the operation's application `outputs`. Command and extension tasks using
the child submission broker get the same metadata without stdout parsing.

The two ID lists have corresponding positions. Multiple jobs sharing a goal
remain separate records; never deduplicate by goal ID or reorder the projections
independently. Empty child lists are empty arrays. Collection supplies separate
provenance such as `result: {goal: G, job: B}` when B is the actual producer.

The protected environment supplies `C2J_CURRENT_TENANT_ID`,
`C2J_CURRENT_JOB_ID`, and `C2J_CURRENT_GOAL_ID`. Recipe context exposes
`context.workflow.goal_id` alongside `context.workflow.job_id`. These identify
the current execution; they are not a mutable list of children. Do not override
them, derive one ID from another, or assume a child shares its parent's goal.

## Restart and operational instructions

Update operator prompts and runbooks to distinguish repair from independent
work. For a recorded restartable scope:

```sh
c2j restart A --scope 'recorded-scope-id' --request-id repair-1
c2j restart A --scope 'recorded-scope-id' --request-id independent-1 --goal new
```

Continue is the default and carries the source's primary goal forward. Reuse
the same request ID and payload for retries of one intended restart; a different
intended attempt needs a new ID. A new shared candidate under a closed goal is
rejected. Never silently fall back to a new goal. Reconciliation of an already
accepted restart still works after closure.

Sharing a parent's goal does not automatically share descendant goals. Retained
child submission records keep their original references; re-executed submissions
follow c2j's restart identity rules. Do not build a second child deduplication
mechanism in shell or prompt logic.

## Repository work and acceptance

Inventory child ops/groups, direct CLI submissions, job-ID-only outputs,
status-polling loops, failure catches, optional work, repair controllers, and
artifact retrieval by original child ID. Update each according to its intent,
including reusable definitions, generated recipes, authoring guidance, prompts,
fixtures, and mocks. Remove old standalone wait input shapes instead of retaining
aliases. Keep exact-job controls explicitly supported for their intended uses.

Pin a goal-aware c2j version with stable workspace replay across restarts. The
core implementation baseline is c2j commits `c351eaf` and `039cadb`. Updated
c2j pins JobDB main commit `86ac742286156ce9f38305b4c8b613df70856095`
(`v0.0.54-0.20261025173555-86ac74228615`) and integrates goal enumeration,
dependency discovery, complete SDK handles, and current execution identity.
Coordinate new submissions with Cortex goal/candidate visibility. The recipe wait/target and automatic
metadata contracts above remain unchanged. Delete old jobs at cutover and
exercise only the new contract.

For operator diagnostics, use goal records and their nonterminal-candidate
counts to find open goals needing attention. Reverse waiter lookup shows only
currently unresolved edges in pending waits. Use the consumer job or known wait
ID for retained history; disappearance from reverse lookup alone does not prove
success. Use `c2j goals list`, `c2j goals waits --target-goal G`,
`c2j goals waits --consumer-job P`, or `c2j goals wait P WAIT` for these reads.
These are tenant inspection surfaces, not new delegated recipe permissions or
an invitation to replace durable waits with polling.

Validate recipes and run representative real JobDB integrations, not only
mocked success fixtures. Required scenarios:

1. A fails, P remains waiting on G, and replacement B sharing G unblocks P with
   B's output/artifacts and recorded provenance, including after worker restart.
2. Exact-job soft inspection returns A's failure/partial results while G remains
   open; the repair controller is not blocked by its own goal-success wait.
3. Explicit goal failure produces the intended required/optional behavior and
   preserves the failure snapshot without cancelling running producers.
4. An optional open goal stays pending; the selected exact-job alternative ends
   after its candidate terminates. Enclosing timeouts bound abandoned waits.
5. Native, command, and extension child submissions expose complete references
   and paired automatic lists, including multiple jobs associated with one goal.
6. Lost-response restart retries return the original reference without duplicate
   candidates; new-goal restarts are explicit, and workspace replay succeeds.
7. Result validation rejects incompatible winning output without substituting a
   different candidate. Review packs retain original and winning identities.

The work is complete when every orchestration boundary has an intentional wait
policy, goal-success consumers survive replacement attempts, exact-attempt
failure handling still works, and tests and authoring guidance use only the new
contract.
