# Requirements: SWF-Go Reschedule Timeout Resume

## Summary

SWF-Go needs a durable timeout-resume mechanism for any job suspension or
reschedule, not just `WaitForJobIDs`.

When execution is parked, SWF should be able to say:

```text
resume this job when the normal wait condition is satisfied
OR
resume this job at the active timeout deadline
```

The timeout resume should lease the base job type/capability, not a job+task
capability. That lets the normal job runner replay durable chapters, pick up
where it left off, detect that the timeout deadline has elapsed, and record the
standard timeout result.

## Problem

SWF correctly avoids keeping goroutines blocked for long waits. When execution
suspends, the current runner exits and the job is parked until the runtime makes
it leaseable again.

The missing behavior is that parked jobs are not always made leaseable again at
their timeout deadline. This can strand jobs past their durable timeout when the
normal wake condition never arrives.

This applies to any suspend/reschedule path, including:

- waiting for child jobs
- waiting for a future time
- waiting for an external task/capability
- waiting for human input or another external completion signal

## Durable Replay Constraint

Timeouts must be based on persisted SWF chapter timing.

Example:

```text
8am   job starts
10am  job suspends waiting for human input
2pm   human input arrives and the job is leased again
```

If the job has a 30 minute total timeout, replay at 2pm must evaluate the
timeout from the original recorded 8am start. It must not receive a fresh budget
because a new runner process started at 2pm.

## Required Model

Every reschedule that can park a job should support an optional timeout resume:

```text
normal wake condition:
  jobs complete, time arrives, external task completes, human input arrives

timeout wake condition:
  active durable task/job deadline arrives

timeout wake target:
  base job type/capability
```

The base job type is the job worker capability, such as `recipe`. It is not an
external task capability such as `recipe:input:collect_user_input`.

This distinction matters because a timeout wake may not have a completed task
chapter. The job runner must replay up to the unfinished task and let the normal
timeout machinery create the timeout chapter.

## Functional Requirements

1. Reschedule requests must be able to express a timeout resume.

   The runtime must persist both:

   - the normal suspended condition
   - the absolute time at which the job should resume for timeout evaluation

2. The timeout resume target must be the base job type/capability.

   On timeout wake, SWF should lease the job to the job worker, not to the
   external task worker/capability that may have been pending.

3. The timeout resume time must be derived from durable runner deadlines.

   For task execution, use the earliest active deadline among:

   - task invocation timeout
   - task total timeout
   - enclosing job total timeout

   For job execution, use the earliest active deadline among:

   - job invocation timeout
   - job total timeout

4. The resume time should be absolute.

   If the public API uses a relative duration, the runtime must persist the
   computed absolute resume time so restarts and clock drift do not create a new
   timeout budget.

5. The normal wake condition must continue to work.

   If the child jobs complete, a future time arrives, or an external task
   completes before the timeout resume time, the job should wake normally.

6. Timeout wake must not implicitly cancel external work.

   Parent timeout and child/external cancellation are separate policies. SWF
   should make the parent leaseable for timeout evaluation, but it should not
   automatically cancel child jobs or external tasks unless a workflow explicitly
   requests that.

7. Runner replay must record the timeout through the existing timeout path.

   The timeout resume should not create a special synthetic task output. It
   should cause the job runner to replay durable chapters and then detect the
   elapsed deadline before executing or rescheduling unfinished work.

8. External task scheduling must check deadlines before re-suspending.

   If replay reaches an unfinished external task after the timeout resume, the
   runner must record the timeout instead of immediately rescheduling the same
   external task again.

9. Existing behavior without active timeout must remain unchanged.

   Jobs without an applicable deadline should continue to park only on their
   normal wake condition.

## API Requirements

SWF-Go should expose a first-class way to express the timeout resume. One
possible shape:

```go
type RescheduleExecutionRequest struct {
    NextNeed string
    Payload  json.RawMessage

    WaitUntil     *time.Time
    WaitForJobIDs []string

    TimeoutResumeAt   *time.Time
    TimeoutResumeNeed string // base job type/capability
}
```

Equivalent designs are acceptable if they clearly represent:

```text
normal suspended condition OR timeout resume at absolute time to base job type
```

The existing `AlternateNeed`/`AlternateAfter` mechanism may be reused only if it
can provide those semantics consistently across all runtimes. In particular, it
must support resuming to the base job capability at the durable deadline.

## Runtime Requirements

All runtime implementations must honor timeout resume semantics:

- toy runtime
- direct runtime
- sqlite runtime
- remote runtime/client/server transport

Each runtime must make a parked job leaseable when either:

- the normal suspended condition is satisfied
- `TimeoutResumeAt` has arrived

When both are available, the first condition to become true should win.

## Runner Requirements

1. The runner must compute the active timeout resume for all suspension paths.

2. `AwaitJobs` should include the timeout resume when child jobs are not yet
   terminal.

3. `AwaitDuration` should continue to clamp waits to the active deadline, or use
   the same timeout resume mechanism if it reschedules.

4. External task/capability waits should include timeout resume to the base job
   type.

5. Job-level suspension should include timeout resume to the base job type when
   a job deadline is active.

6. Task-level suspension should include timeout resume to the base job type when
   a task or enclosing job deadline is active.

## Acceptance Criteria

1. A job parked waiting for child jobs becomes leaseable at its timeout deadline
   even if the child jobs are still running.

2. A job parked waiting for human input becomes leaseable at its timeout
   deadline even if no input has arrived.

3. A job parked waiting for an external task/capability becomes leaseable at its
   timeout deadline even if the external task has not completed.

4. When the normal wake condition happens before the timeout, the job resumes
   normally.

5. When the timeout resume happens first, the job runner replays durable
   chapters and records the standard SWF timeout result.

6. A replay after a long parked interval uses the original persisted start time,
   not the runner restart time.

7. No runtime keeps a goroutine blocked for the full parked duration.

8. All supported runtimes pass shared conformance tests for timeout resume.

## Non-Goals

- Do not implement consumer-side polling as a substitute for SWF timeout resume.
- Do not create a second timeout system outside SWF run policy and chapter
  timing.
- Do not automatically cancel child jobs or external tasks on timeout.
- Do not change successful reschedule behavior when no timeout is active.
