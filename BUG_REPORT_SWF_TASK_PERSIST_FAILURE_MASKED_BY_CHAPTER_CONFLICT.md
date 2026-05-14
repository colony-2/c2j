# BUG: SWF masks task outcome persistence failure with next-ordinal story conflict

## Summary

When a recipe task fails and SWF cannot persist that failed task outcome chapter, `workerRunner` advances to the next story ordinal and then attempts to persist a job outcome. Because the failed task chapter was never written, the job-outcome write fails with a secondary append conflict such as:

```text
workflow state conflict: chapter ordinal 4 is not appendable; expected 3
```

This masks the real failure and can leave the job `ACTIVE` instead of reaching a terminal failed state.

This was observed through c2j embedded execution after an extension failed before it could run because Docker/Podman was unavailable:

```text
create runner: failed to create docker client: unable to find docker socket; set DOCKER_HOST or ensure Docker/Podman is running
story: unexpected status 400 uploading chapter
workflow state conflict: chapter ordinal 4 is not appendable; expected 3
```

## Versions

- c2j: current `main` around commit `15b2967`
- swf-go: `github.com/colony-2/swf-go v0.0.0-20260413043057-cd8d41023399`
- strata-go: `github.com/colony-2/strata-go v0.0.0-20251215181525-c51f2cd6b43e`
- Runtime path: c2j `embed:///`, SWF direct runtime, embedded Strata daemon

## Expected

If a task operation fails, the recipe job should complete terminally as failed and preserve the original task error when the failed task outcome can be persisted.

If persisting the failed task outcome itself fails, SWF should not advance the story ordinal and should not attempt to persist a later job outcome chapter. It should return the persistence failure directly, or complete using a valid recovery path that does not create a skipped ordinal.

In no case should a missing task outcome chapter be masked by a later `chapter ordinal N is not appendable; expected N-1` conflict.

## Actual

The runner returns a later story append conflict after the task outcome chapter failed to persist. The original task failure and/or the original chapter-upload failure is obscured. The job can remain `ACTIVE`.

## Why this appears to be SWF-side

The SWF runner increments `storyCounter` before persisting a task outcome:

- `workerRunner.DoTask(...)` selects `ordinal := r.storyCounter` and increments `r.storyCounter`.
- On task failure, it builds an error payload and calls `persistTaskDataChapter(...)` for that task ordinal.
- If `persistTaskDataChapter(...)` returns an error, `DoTask(...)` returns that error.
- `workerRunner.DoJob(...)` receives that error as `jobErr`, then selects `ordinal := r.storyCounter` again. Since the counter was already incremented, this is the next ordinal.
- It then tries to persist the job outcome at `N+1`, even though chapter `N` never successfully persisted.

The SWF direct runtime correctly rejects that write as non-appendable because Strata’s latest visible story chapter is still `N-1`.

## Why this does not look like a normal async Strata delay

In the embedded Strata path, `UpsertChapter` calls `rows.AppendChapter(...)` and returns only after that call succeeds. The Pebble rowstore writes both the chapter row and story metadata in one batch and calls `batch.Commit(pebble.Sync)` before returning.

So the stronger immediate issue is not that `SaveChapter` intentionally returns before the write is complete. The observed conflict is consistent with the prior chapter write failing, followed by SWF trying to write the next ordinal anyway.

## Secondary Strata diagnostics issue

The Strata client upload path translates `404` and `409`, but not `400`. A server-side bad request is surfaced as:

```text
story: unexpected status 400 uploading chapter
```

That drops the response body, which likely contains the actionable reason the failed task outcome chapter could not be saved. The client should translate `400` JSON error responses the same way it translates `404` and `409`.

## Suggested fixes

1. In SWF `workerRunner`, distinguish task execution errors from task outcome persistence errors.
2. If persisting task outcome chapter `N` fails, do not try to persist a job outcome at `N+1`.
3. Preserve the original task error only when the failed task outcome chapter was successfully persisted.
4. Add a regression test where `PutChapter` fails for a failed task outcome and assert no later job outcome `PutChapter` is attempted.
5. In Strata client `uploadChapter`, translate `400` error bodies so callers see the real chapter upload failure.

## Suggested SWF test shape

Use a test runtime where:

- initial chapters exist through ordinal `N-1`
- the task worker returns `output, appErr`
- `PutChapter` for task outcome ordinal `N` returns a synthetic storage/upload error

Assert:

- `DoJob` returns that synthetic persistence error, not an append conflict
- `PutChapter` is not called for ordinal `N+1`
- the lease is not completed as if a job outcome was persisted

## Current c2j workaround

c2j commit `15b2967` added a runtime wrapper that waits for a written chapter to become readable before allowing the next ordinal write. That can reduce read-after-write races, but it is not the root fix for this SWF failure path. Once SWF handles failed task-outcome persistence correctly, the workaround should be revisited or removed.
