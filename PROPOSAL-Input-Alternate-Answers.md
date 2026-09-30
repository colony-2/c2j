# Proposal: default answers through an input alternate task

Status: implemented with a companion JobDB runner change; see [the user guide](GUIDE-Input-Fallback-Answers.md).

## Recommendation

Keep one `input` op in the recipe. Register an internal task handler such as
`input-alternate:complete` that can supply configured answers to the same pending
input task once its waiting period has elapsed. Use JobDB's alternate route to
make that handler eligible, and persist its result through the ordinary task
execution path.

Reuse the existing op substep chain, answer validators, autofill logic, artifact
handling, and JobDB history. Add one optional delayed alternate for a pending
substep. Do not introduce a general branching language, a polling op, a child job,
or a recipe-visible preparation step.

Fix JobDB's external-task timeout behavior separately. A timeout fix is necessary
for hard deadlines, but does not by itself implement successful fallback answers.

## Recipe experience

Proposed syntax:

```yaml
- id: approval
  op: input
  inputs:
    form:
      title: Deployment decision
      fields:
        - id: decision
          type: multiple_choice
          question: Proceed with deployment?
          required: true
          options:
            - value: approve
              label: Approve
            - value: defer
              label: Defer
    if_unanswered:
      after: 30m
      fields:
        decision: defer
```

`if_unanswered` accepts the existing answer shapes: `fields` for a multi-question
form, or `response` for a single question/structured response, plus ordinary
artifact references where applicable. It does not accept caller-selected task
names. Durations must be positive. Without this block, existing behavior remains.

This also applies to `form.kind: review`. Documents and returned attachments use
the existing artifact mechanism. The fallback does not invent annotations or
pretend that a human reviewed a document.

Templates resolve during form preparation. Freeze the fallback answers and an
absolute `fallback_at` in the durable prepared result, calculated from its recorded
preparation time plus `after`. Validate the configured answers before publishing
an input request; invalid required fields or invalid choices must fail early.
Retries and worker restarts reuse this recorded deadline and these answers.

The normal op output remains unchanged for downstream answer references. Record
that the answer was supplied automatically in the receipt/metadata, with a system
actor and a stable submission ID derived from the input occurrence. Existing field
`default` values still mean omitted-answer defaults; they do not start a timer.
Immediate `autofill` retains its meaning and is mutually exclusive with
`if_unanswered` for the first version.

## Execution model

The existing input chain is `generate_form -> collect_user_input`.
`collect_user_input` deliberately has no local task worker. Extend its handoff as
follows:

| Item | Value |
| --- | --- |
| Logical pending task | `input:collect_user_input` |
| Normal route | `{JobType: recipe, TaskType: "input:collect_user_input"}` |
| Delayed alternate route | `{JobType: recipe, TaskType: "input-alternate:complete"}` |
| Alternate due time | Frozen `fallback_at` |
| Durable result | The original pending task's output slot and input identity |

1. `generate_form` persists the form, fallback policy, and ordinary artifact refs.
2. The compiler requests the logical collection task with its optional alternate.
   The scheduler receives the same task coordinates for both routes.
3. A human response can complete the normal pending task through the existing
   input API.
4. Once the alternate is due, a worker advertising `input-alternate:complete` can
   acquire it. Replay reaches the same logical collection task and invokes the
   selected alternate handler using the frozen collection input.
5. The alternate handler validates/builds the fallback output using shared input
   acceptance logic. The runner records a successful result for the logical
   collection task. Subsequent replay consumes that result and continues normally.

The alternate handler is not an extra sequential task after collection; collection
would otherwise never finish. It is another implementation of that one pending
substep. It must not call the external response API to complete its own leased
work: it returns an ordinary task output to the runner.

## Small extensions needed

### 1. Carry a delayed alternate alongside the next substep

c2j already records `NextTask` in activity results. Add an optional descriptor for
that next task: an alternate task type and an absolute due time. The input op's
preparation step supplies it internally. Persist it with the prepared result so
replay cannot restart the waiting period or recalculate answers.

The compiler forwards the descriptor into JobDB's task invocation options. JobDB
turns it into the existing `AlternateRoute` and `AlternateAfter` when handing off.
Compute the latter from the remaining time until the frozen deadline, clamped to
zero. Do not restart the full duration on every reschedule.

This needs a small public task-options extension in JobDB; the exact Go API should
be chosen there. Keep it distinct from hard timeout/retry policy. No new scheduler
route primitive is needed for the external-input case.

### 2. Honor the selected handler during replay

Registering `input-alternate` alone is insufficient today: JobDB's `DoTask` looks
up the original requested task type, and otherwise reschedules that original task
again. Extend the shared runner to recognize an explicitly declared alternate
for the current pending task.

Only select it when the lease's effective route, task coordinates, and input hash
match this invocation and its recorded alternate declaration, and its deadline is
due. Merely having the alternate worker registered must never execute fallback
early or redirect another substep encountered during replay.

Keep the original logical task identity in durable history and schema validation;
the alternate handler must satisfy the same output contract. Handler identity and
fallback provenance can be recorded separately. Completed output wins on replay,
regardless of which handler originally produced it. Never relabel a result after
it has been stored.

Renewal/import must retain the selected route and coordinates. This is already a
requirement of JobDB's supplied-lease support; backend adapters must honor it too.

### 3. Add the input alternate handler

Register `input-alternate:complete` with the normal c2j workset. Factor/reuse the
existing `auto-fill-input:echo` and form/review/structured acceptance helpers rather
than build a second response validator. Preserve workspace, git state, attachments,
and the activity output envelope exactly as other input completions do.

Use frozen request identity and answers. Repeat execution after a crash must not
create a second input occurrence or overwrite a completed answer. Expose
`fallback_at` to input clients so a UI can explain the pending fallback; no new UI
workflow is necessary.

## Relationship to hard timeouts

`if_unanswered.after` is a successful-answer policy, not the op's `timeout`.
A recipe/op/job hard deadline can still fail execution and must not be extended
by fallback. If an unfinished task has already exceeded a hard deadline, timeout
handling takes precedence over either handler. Authors should leave sufficient
hard-timeout budget for fallback to execute.

The JobDB fix must do more than move a check above the missing-worker branch:

- Replay completed task outcomes first; elapsed time must not invalidate them.
- For unfinished work, check applicable durable hard deadlines before dispatching
  locally, choosing a fallback, or handing the task off again.
- Make timeout recovery eligible at the earliest applicable hard deadline, rather
  than always using a fresh invocation-timeout delay.
- Persist timeout outcomes at the correct history position, without colliding with
  already completed task chapters.

There is only one scheduler alternate. When a hard deadline is earlier than the
fallback, schedule the job-only timeout-recovery route instead. When fallback is
earlier, schedule the alternate handler; on execution, still check hard deadlines
in case scheduler delay has allowed them to expire. Preserve an absolute fallback
deadline across intermediate recovery wakeups.

Dependency waits and `WaitUntil` eligibility are separate JobDB issues. This proposal
addresses an external input-task wait; it does not claim to solve those other waits.

## Race and timing semantics

Agreed behavior: **the delay makes fallback eligible. Human answers are not rejected
merely because that time has passed.**

If the human response completes the pending input first, the flow advances and
there is no pending input left for the alternate handler to acquire. If the
alternate handler acquires the pending input first, it completes through its lease;
a competing human submission follows the existing ownership/conflict rules. Neither
path can overwrite a durable answer. Concurrent attempts to obtain completion
authority use JobDB's existing conditional ownership machinery.

Both paths must use JobDB's conditional ownership and task-output arbitration,
never a check-then-unconditionally-write sequence. Recovery after an ambiguous
write must consult the original output slot. A crash after claiming fallback must
recover the same occurrence using the frozen policy and ordinary lease expiry.
This provides one durable answer, not exactly-once external side effects.

Actual fallback execution requires an available worker and may happen after the
configured time. There is no timer service generating answers independently of
workers.

## Validation before shipping

- Normal response before fallback: unchanged output; fallback never executes.
- Human response after the alternate becomes eligible but before it acquires the
  pending input: accept the response, advance the flow, and verify the alternate
  can no longer acquire that occurrence.
- No response: alternate becomes claimable, supplies valid answers, and the next
  recipe node runs. Registering the handler cannot make it execute early.
- Restart/replay before and after the deadline: no timer reset, changed answers,
  duplicated results, or task identity/schema mismatch.
- Human and fallback completion race, including an ambiguous response and a crash
  after acquisition: one durable answer, no overwrite, correct recovery/provenance.
- A completed input replays after its deadline without turning into a timeout.
- Earlier hard deadlines fail normally; fallback cannot bypass them. Existing
  external-task timeout and history-collision reproductions become regression tests.
- Repeated input occurrences in sequences/states remain distinct. Ordinary forms,
  reviews, structured responses, and stored attachments follow the same validation.
- Alternate acquisition followed by supplied-lease import/renewal keeps the chosen
  handler and task coordinates. No matching worker means pending work, not success.

Run the lifecycle tests against persistent SQLite and remote JobDB, and require
adapter coverage for any production backend used to schedule alternates.

## Source observations

This proposal is based on JobDB v0.0.22 and the current c2j checkout:

- [Input's existing two-step registration](pkg/input/activity.go).
- [Existing autofill handler](pkg/input/auto_fill.go).
- [Task-step registration](pkg/ops/registerable_op.go) and
  [durable next-task output](pkg/worker/ops/activity_registry.go).
- [Compiler substep execution/replay](pkg/worker/compiler/compiler.go).
- [JobDB handoff and timeout ordering](https://github.com/colony-2/jobdb/blob/v0.0.22/pkg/workflow/worker_runner.go).
- The external-task timeout gaps described above are covered by the companion JobDB regression tests.
