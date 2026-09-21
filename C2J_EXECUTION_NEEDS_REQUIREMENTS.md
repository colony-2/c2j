# c2j: execution needs on recipe nodes

Enhance existing recipe nodes in `github.com/colony-2/c2j` to use c2j's existing
execution-control capabilities. Inspect the existing node, template, execution,
and task-replay machinery before choosing implementation details.

This is an additive recipe-node feature, not a replacement for existing
execution APIs or declarations. Do not introduce a resource op or a new
execution-scope construct. Dependency changes, storage redesign, and migration
work are not requirements of this feature. Automatic resource downsizing is
out of scope.

## Node properties and templates

Add optional `execution_needs` to existing recipe nodes and scopes, including
recipe roots, sequences, states, individual operations, inline inclusions, and
child groups. Support the existing execution properties: CPU, memory, usable
temporary storage, image, and platform (OS/architecture/variant).

- Every value is templatable using c2j's existing template rules. This feature
  introduces no new expression syntax, evaluation context, or timing rules.
- Inherit properties from enclosing nodes. The closest explicit value wins
  **per property**; omitted properties inherit. Do not take the maximum of
  parent and child numeric requirements.
- All nested scopes inherit within the same job. Inner settings apply only
  within their scope and are dropped when that scope is exited. They must not
  become permanent job-level overrides.
- Separate jobs have no inheritance. A child group's settings apply to its
  work in the containing job, not to the separately submitted child jobs.
- Use existing execution validation, quantity normalization, and image/platform
  matching behavior for resolved values.

For example, a sequence requesting CPU 4, memory 16 GiB, image A, and
linux/amd64 can contain an operation requesting memory 8 GiB, image B, and
linux/arm64. That operation inherits CPU 4 and uses its own other values.
Its next sibling again inherits the sequence's values. The inner operation's
memory could instead be an expression referencing an earlier operation's output.

## Execution and handoff

Before executing a task that has not already completed, determine its effective
needs from the applicable scopes and compare them with the executor's actual
environment:

1. If satisfied, execute normally. Numeric requirements are minimum capacities;
   an oversized allocation is acceptable. Do not yield just to shrink it.
2. If unmet, reschedule/yield the same job with the resolved needs through the
   existing execution-control mechanism. Do not execute the task or report
   this handoff as task/job failure.
3. An external controller provisions/selects a suitable executor. On resumption,
   replay the recipe using the existing task history, then verify the actual
   environment before executing unfinished tasks.

Unknown allocation facts cannot satisfy explicit requirements. Image/platform
changes can require a handoff even when CPU and memory are sufficient. A flow
must support amd64 → arm64 → amd64 as nested overrides enter and leave effect.
Deferring downsizing does not defer those image/platform transitions.

Apply environment checks at the next unfinished task, using its full inherited
and overridden needs. Entering a containing sequence must not trigger a handoff
for its defaults before considering that task's overrides. Exiting an inner
scope restores the enclosing needs, but requires a handoff only if the next
unfinished task cannot run in the actual environment.

## Recovery and replay

- Recipes are rerun on recovery using c2j's existing per-task cached inputs
  and completed results. Scope inheritance is reconstructed as the recipe is
  replayed.
- A completed task must use its recorded result without re-executing or
  requesting its historical environment. This also applies to completed tasks
  within partially completed operations or containers.
- Environment rescheduling is needed only for an unfinished task whose needs
  are not satisfied. Replaying enclosing scopes or completed tasks must not
  create a handoff loop.
- Repeated node occurrences, retries, and skipped work follow existing recipe
  and task-replay behavior. Skipped work must not cause environment handoffs.
- Reuse existing handoff durability, cancellation, and lease-fencing guarantees.
  A lost handoff response must not allow work to continue under uncertain
  ownership. This feature does not require per-chapter scheduler updates or a
  separate persistence model for expression evaluation.

## Listing visibility

- Present execution requirements only for jobs waiting for execution or
  resumption, using the requirements recorded for that wait.
- Do not present previously recorded requirements as the current needs of a
  running job. Its effective needs may be held ephemerally and change as it
  progresses through scopes without yielding.
- Listings must not imply that running jobs have no requirements merely because
  their current needs are not exposed. Do not use a stale recorded requirement
  to claim compatibility for a running job.
- No live publication or tracking of running jobs' changing needs is required.

## Acceptance tests and deliverables

- Literal and templated values for every supported property, following existing
  template and validation rules; prior outputs can determine a later task's needs.
- Nested per-property inheritance, including a smaller child numeric value,
  and restoration of parent defaults for later siblings across all same-job
  scopes. Separately submitted jobs do not inherit these settings.
- Satisfied/oversized environments run without handoff; insufficient or unknown
  capacity yields before the unfinished task performs work.
- Image/platform overrides move the same flow between environments and back.
- Yield/resume preserves job identity and completed outputs; the pending task
  runs only after its environment is suitable.
- Recovery replays a partially completed operation without requesting the
  environments of its completed tasks, and checks its next unfinished task.
- Replay, repeated node occurrences, and crash/lost-response recovery do not
  resize for completed work or create handoff loops. Skipped work does not yield.
- Waiting-job listings expose recorded requirements. Running-job listings do
  not present stale requirements as current or use them to assert compatibility.

Deliver implementation, focused tests plus relevant existing integration tests,
and short consumer guidance with actual c2j syntax for static and templated
needs, inheritance, executor configuration, recovery, and waiting-job listings.
Keep changes focused on these node enhancements and their integration with
existing execution capabilities.
