# Requirements: c2j Adoption of SWF-Go Reschedule Timeout Resume

## Summary

c2j should rely on SWF-Go's durable timeout-resume behavior for any job
suspension or reschedule. c2j should not add polling loops or a second timeout
system around child jobs, human input, or external task waits.

Once SWF-Go can resume parked jobs at their durable timeout deadline, c2j should
ensure every recipe and op timeout is represented as SWF run policy and that all
op execution receives the SWF-derived cancellation context.

## Dependency Requirement

c2j must upgrade to a SWF-Go version that supports timeout resume for suspended
jobs.

That SWF-Go version must make parked jobs leaseable at the active durable
timeout deadline by resuming to the base job type/capability.

## c2j Execution Model

c2j should preserve a single timeout model:

```text
recipe/test timeout overlay
  -> recipe root timeout
    -> sequence/state timeout
      -> op node timeout
        -> op metadata default timeout
          -> swf.RunPolicy
            -> SWF durable timeout resume
              -> task context cancellation
                -> op/process termination and timeout result
```

The timeout source of truth is SWF run policy and persisted chapter timing.

## Functional Requirements

1. Recipe root timeout must be submitted as SWF job total timeout.

   This lets SWF compute timeout deadlines from the original job start chapter
   and resume parked jobs at the correct time.

2. Op node timeout and op default timeout must be submitted as SWF task total
   timeout.

   Explicit node timeout wins over op default timeout.

3. Composite timeouts must clamp nested task timeouts.

   Sequence and state-machine timeouts should constrain nested op execution by
   reducing the SWF task timeout to the active parent budget.

4. c2j task workers must pass a SWF-derived context to op execution.

   The context should be canceled when the SWF task context reports that the
   durable deadline has elapsed. It must not be based on a fresh c2j wall-clock
   timeout that would reset during replay.

5. Restore, op invocation, artifact materialization, and persist should all use
   the SWF-derived context.

6. Built-in blocking ops should use the provided context.

   This includes:

   - `command_execution`
   - `extension_execution`
   - `sleep`
   - git-backed ops
   - recipe source resolution tasks
   - within-recipe selector resolution tasks

7. `recipe.await_result`, `recipe.run_and_get_result`, and
   `recipes.run_and_wait` should continue to use SWF job waiting primitives.

   c2j should not replace these with workflow-control polling. SWF-Go should
   handle timeout resume for the suspended parent job.

8. Human input waits should continue to use SWF external task/capability
   mechanics.

   When SWF-Go supports timeout resume, a recipe parked waiting for human input
   should resume to the base recipe job type at the timeout deadline, replay,
   and record the existing timeout result.

9. c2j test framework timeouts should overlay SWF execution timeouts.

   A case timeout should become a root recipe/job timeout overlay, not a separate
   watchdog. The overlay should be visible to SWF so suspended jobs can resume at
   the case deadline.

10. Timeout wake must not cancel child jobs implicitly.

    If c2j wants child cancellation when a parent times out, that should be an
    explicit recipe/runtime policy handled separately.

## Required Updates After SWF-Go Support

1. Bump the `github.com/colony-2/swf-go` dependency to the version containing
   timeout resume support.

2. Update any c2j call sites impacted by the new SWF-Go reschedule API.

3. Confirm recipe worker registration uses the base recipe job type as the
   timeout resume target for task/external waits.

4. Confirm task workers do not encode timeout durations into task input payloads.

   Task payloads participate in deterministic replay. Timeout metadata should
   stay in SWF run policy/chapter metadata.

5. Confirm input/human waits and child-job waits rely on SWF resume semantics,
   not c2j polling.

6. Add recipe-level and integration tests that exercise suspended timeout
   behavior.

## Acceptance Criteria

1. Parent waiting on child job IDs times out.

   A parent recipe with a short timeout and a child job that never finishes
   should resume at the parent timeout deadline and record the standard timeout
   result.

2. Parent waiting on human input times out.

   A recipe waiting on an input collection step should resume at the timeout
   deadline and record the standard timeout result without any submitted input.

3. External task wait times out.

   A recipe parked on an external task/capability should resume to the base
   recipe job type at the timeout deadline and record a timeout instead of
   re-scheduling the external task indefinitely.

4. Normal wake still works.

   If a child job completes, human input arrives, or an external task completes
   before timeout, the recipe should resume and complete normally.

5. Replay preserves original timing.

   A recipe that starts at 8am, parks at 10am, and is leased again at 2pm should
   evaluate timeout from the persisted 8am start time.

6. No c2j-side polling is introduced for suspended jobs.

7. Op process cancellation still terminates process trees when the SWF-derived
   context is canceled.

8. Full c2j test suite passes with the upgraded SWF-Go dependency.

## Test Coverage Requirements

c2j should add tests covering:

- child job wait timeout
- human input wait timeout
- external task/capability wait timeout
- normal child/input/external completion before timeout
- replay after a parked interval uses original persisted timeout timing
- task payload determinism is unchanged by timeout metadata
- process-based ops exit when the SWF-derived context is canceled

## Non-Goals

- Do not implement c2j-side polling around child job status.
- Do not keep long-lived goroutines blocked while waiting for children or human
  input.
- Do not serialize timeout deadlines into activity invocation input.
- Do not create a separate test harness cancellation system with different
  semantics from normal recipe execution.
