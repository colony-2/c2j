# Bug: unfinished external tasks outlive their total timeout and are re-suspended

## Status as of 2026-10-02

This is a historical report. The pinned JobDB v0.0.23 fixes the external-task
handoff deadline and terminal-chapter collision described below. Current tests
assert successful timeout completion and history preservation; they no longer
assert the old bugs. The chapter test is now named
`TestTimeoutReproJobDeadlinePreservesExistingChapter`.

The separate dependency-wait eligibility requirement remains open. See
[the current investigation](EXTERNAL_INPUT_TIMEOUT_INVESTIGATION.md) for the
verified behavior and commands. The reproduction narrative and old test name
below describe the originally affected version.

## Affected version and impact

Reproduced with JobDB `v0.0.19-0.20260919034646-71b6668a65db`
(`71b6668a65dbcc08ae46119412c559f54d8a4b32`), using its toy runtime, SQLite
runtime, and SQLite exposed through the HTTP runtime. PostgreSQL was not tested.

A workflow suspended on a task with no locally registered worker does not
reliably resume for timeout handling at its total deadline. Even after its
alternate route becomes eligible, the runner can re-suspend the expired task
instead of recording its timeout. This affects unanswered input, approvals,
and other externally completed tasks. It does not require a late response,
a browser, or any particular workflow client.

## Minimal reproduction

Use a workflow job with:

- Job total timeout: 5 seconds; invocation timeout: 2 seconds.
- One task named `external`, with total timeout 100ms and invocation timeout
  600ms. Register the job worker but no task worker for `external`.
- Retry maximum attempts: 1, to exclude retry policy as the explanation.

The job worker simply executes:

```go
return ctx.DoTask(taskPolicy, "external", input)
```

1. Run the job once. It suspends on route `{JobType: "repro", TaskType: "external"}`.
2. Observe the handoff's alternate job route and `AlternateAfter = 600ms`.
3. At approximately 160ms after handoff, try to acquire/run the job with the job
   route. No lease is acquired, although the task's total deadline has passed.
4. At approximately 650ms, acquire/run it again. The alternate route now permits
   acquisition, but the runner suspends the same expired task again, scheduling
   another 600ms alternate delay. No timeout completion is recorded.

Executable reproductions in the accompanying c2j checkout use JobDB directly;
the primary test does not execute a c2j recipe:

```bash
GOTOOLCHAIN=go1.26.1 go test -tags=timeoutrepro ./pkg/worker/compiler \
  -run '^TestTimeoutReproJobDBExternalTaskDeadline$' -v -count=1
```

Sources: `pkg/worker/compiler/external_timeout_repro_test.go` and
`pkg/worker/compiler/timeout_test_helpers_test.go`. At the time of this report these were characterization
tests. They now assert the fixed behavior, as described in the status note above.

## Confirmed cause

In `pkg/workflow/worker_runner.go`, `DoTask` computes the task's durable
`totalDeadline`, but the missing-local-worker branch (around lines 886–917):

- sets `AlternateAfter` from the invocation timeout without bounding it by the
  applicable total deadline;
- reschedules and exits before the total-timeout check around line 922.

Consequently, the job route is unavailable at the total deadline, and subsequent
replay can take the same handoff branch again without recognizing expiry.

## Required behavior

- An unfinished external task must become eligible for timeout handling at the
  earliest applicable task/job deadline, allowing ordinary scheduling latency.
- The timeout handler must not require the unavailable external task worker.
- Once an unfinished task's total deadline has passed, replay must report its
  non-retryable total timeout instead of restarting the wait.
- Previously completed task results must remain replayable. Moving the timeout
  check ahead of all cached-result handling would introduce a different bug.
- The job must be able to commit the timeout as its terminal outcome, preserving
  existing chapters and lease exclusivity. Cancellation is not a substitute.

The existing alternate-route mechanism appears sufficient for the external-only
case. Deadline selection and expiry handling in the workflow runner need to
honor the above contract; no particular implementation is required here.

## Related blocker: terminal timeout collides with existing history

An independent JobDB-only reproduction first completes a local task, then waits
on an external task until the durable job deadline expires. On recovery,
`DoJob` checks the job deadline before task replay advances `storyCounter`.
Its final timeout write targets ordinal 1, already occupied by the local result:

```text
chapter ordinal 1 already exists with different contents
```

The completion fails, leaving the job nonterminal (`ACTIVE` immediately after
the failed run). This must be addressed for the external timeout fix to reliably
finish workflows that already have task history.

```bash
GOTOOLCHAIN=go1.26.1 go test -tags=timeoutrepro ./pkg/worker/compiler \
  -run '^TestTimeoutReproJobDeadlineConflictsWithExistingChapter$' -v -count=1
```

The control `TestTimeoutReproJobDeadlineEventuallyRecordsFailure`, without prior
task-result chapters, successfully records `failed_timeout`, lifecycle status
`COMPLETED`, and a durable `TIMEOUT` outcome with scope `total`. The completion
API exists; this is a replay/history handling defect.

## Separate scheduling-contract question: dependency waits

The broader requirement is that a waiting job be retrievable when its normal
condition is satisfied **or its deadline expires**. `AwaitJobs` currently
publishes dependency IDs without a deadline wake-up. Explicitly supplying a due
alternate route still does not bypass unresolved dependencies or a later
`WaitUntil`: both targeted acquisition and polling exclude the job.

Reproductions:

```bash
GOTOOLCHAIN=go1.26.1 go test -tags=timeoutrepro ./pkg/worker/compiler \
  -run '^TestTimeoutRepro(AwaitJobsDoesNotScheduleDeadline|CoreAlternateRouteDoesNotOverrideWaits)$' \
  -v -count=1
```

Please clarify/support deadline-driven eligibility independent of ordinary wait
gates, with valid lease ownership and terminal-state protections. A worker can
then use the existing completion API to record the typed timeout. Merely marking
a row expired or changing its route is insufficient if it remains unclaimable
or has no durable terminal outcome. This capability question is separate from
the external-only workflow bug above.

## Suggested regression coverage

1. Task total timeout shorter than invocation timeout, no external response.
2. Job total timeout earlier than the task/invocation deadline.
3. Recovery after expiry does not publish another external-task wait.
4. Cached completed tasks remain replayable after their original deadline.
5. Terminal timeout is recorded both with and without existing task history.
6. Worker restart does not extend deadlines.
7. Equivalent direct-runtime and HTTP behavior; backend parity including
   PostgreSQL when available.
8. Dependency/future wait eligibility at deadline, as a separate core contract.

No JobDB code or dependency version was changed for this report.
