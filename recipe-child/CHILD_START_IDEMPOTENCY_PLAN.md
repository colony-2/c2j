# Plan: Idempotent Child Recipe Starts

## Problem

`recipe.run_and_get_result` and `recipes.run` currently start child jobs without an explicit child `JobID`.

That means this failure window produces duplicate children:

1. the parent task starts one or more child recipes
2. the worker/node dies before the parent task output is persisted
3. the parent task reruns
4. the rerun starts a second set of child jobs

This is especially bad for multi-child fan-out because the rerun can partially overlap the first attempt.

## Goals

- The same parent job + same op invocation + same recipe index must always map to the same child job ID.
- A duplicate submit for that child job ID must return or repair the existing child job instead of creating a second one.
- The parent `start` step must be able to reconstruct the same `job_id` / `job_ids` output after a crash without extra persisted bookkeeping.
- The fix must work for both single-child and multi-child ops.

## Proposed Design

### 1. Add explicit child `JobID` support to the existing start-job seam

`starter.StartRecipeJobWithOptions(...)` already supports `JobID`, but `recipe-child` cannot currently reach it because `workflowctl.StartJob` does not carry a job ID.

Change the shared start path so `recipe-child` can request a deterministic child ID:

- add `JobID string` to `recipe-core/pkg/workflowctl.StartJob`
- update `recipe-worker/pkg/workflow/control.go` to call `starter.StartRecipeJobWithOptions(...)` with `JobID: req.JobID`
- keep current behavior for all callers that leave `JobID` empty

This is the minimum plumbing needed to let `recipe-child` opt into idempotent starts.

### 2. Expose deterministic invocation identity to ops

The child ID should be based on the current recipe invocation, not on user-supplied fields.

Add invocation access to op implementations:

- extend `recipe-core/pkg/ops.OpDependencies` with something like `Invocation() contextual.Invocation`
- populate that field in `recipe-worker/pkg/ops/op_executor.go` from `ActivityInvocationRequest.GitTaskContext.NodePath` and `InvokeSeq`
- keep using `deps.JobTool().GetJobKey()` for the parent job ID

This avoids leaking internal retry identity fields into the user-facing op schemas.

### 3. Generate deterministic child job IDs inside `recipe-child`

In `recipe-child/pkg/recipe/launcher.go`, compute each child `JobID` from this tuple:

- parent job ID
- current invocation path
- current invocation sequence
- relative recipe index within this specific task execution

Recommended shape:

- canonicalize the tuple into a stable byte/string form
- hash it with `sha256`
- render the resulting deterministic bits in the same KSUID-style string format already used for job/project/etc IDs in this repo
- do not introduce an ad hoc `child_<hex>` or other one-off child-only ID format

Rules:

- single-child ops always use recipe index `0`
- multi-child ops use the resolved recipe slice order, so indices remain `0..n-1`
- do not rely on recipe name for identity; if the same slot replays with different content, the runtime equivalence check should reject it as a conflict

This gives us a stable child ID for every fan-out slot without any extra database state.

### 4. Rely on confirmed `swf-go` explicit-`JobID` idempotency

This plan assumes the `swf-go` team’s confirmed behavior for explicit `JobID` submits:

- an equivalent resubmit for the same `{tenant, JobID}` returns the existing handle
- a conflicting resubmit for the same `{tenant, JobID}` returns an error instead of silently reusing the job
- the partial-submit failure window is already handled by the runtime

That means this server-side plan does not include `swf-go` implementation work. It only depends on those runtime semantics being available to the direct runtime path the server already uses.

## Failure Handling After The Fix

With the above in place, the current crash scenario becomes safe:

1. parent `start` step computes deterministic child IDs
2. first attempt submits some or all child jobs
3. worker dies before parent task output is persisted
4. parent task reruns
5. rerun recomputes the same child IDs and resubmits them
6. runtime returns existing handles for already-submitted children and only creates missing ones
7. `recipe-child` returns the same `job_id` / `job_ids` payload again
8. downstream `finish` / `await_result` steps either wait for the existing child output or fetch the already-completed result

This also fixes partial fan-out:

- if child `0` was created and child `1` was not, the retry reuses `0` and creates only `1`
- the parent still gets a stable ordered `job_ids` array

## Concrete Changes

### In this repo

- `recipe-core/pkg/workflowctl/types.go`
  - add `JobID` to `StartJob`
- `recipe-core/pkg/ops/op_dependencies.go`
  - add invocation accessors and builder wiring
- `recipe-worker/pkg/ops/op_executor.go`
  - pass invocation metadata into op dependencies
- `recipe-worker/pkg/workflow/control.go`
  - forward `StartJob.JobID` into `starter.StartRecipeJobWithOptions(...)`
- `recipe-child/pkg/recipe/launcher.go`
  - add deterministic child ID helper
  - set `StartJob.JobID` for every child launch
  - keep fan-out ordering stable by recipe index
- `recipe-child/pkg/recipe/op.go`
  - thread invocation-aware start context into single and multi-child starts
- if the existing shared ID helpers are not directly usable from `recipe-child`, add or expose a small shared helper for KSUID-formatted deterministic IDs rather than importing another module’s `internal/idgen` package

### Optional follow-up

- `pkg/swf/runtime/remote/runtime.go` currently rejects custom job IDs
- if `recipe-child` needs this idempotency over the remote runtime path later, that API will need a companion change

## Test Plan

### `recipe-child` unit tests

- same parent job + invocation + recipe index => same child job ID
- different recipe index => different child job ID
- different invocation path or sequence => different child job ID
- single and multi-child starts pass explicit `JobID` to `WorkflowControl.StartJob`

### `recipe-child` integration/regression tests

- rerun the `start` step and verify the returned `job_id` / `job_ids` are unchanged
- multi-child retry after partial creation returns the same ordered `job_ids`

### End-to-end regression

- reproduce the current crash window: child submit succeeds, parent task output is not persisted, parent reruns
- verify the rerun does not create a second child job and the parent settles using the original child job output

## Notes

- This plan intentionally avoids parent-side “started child job” persistence. Deterministic child IDs plus idempotent duplicate-submit handling are enough to reconstruct the parent step output.
- We do not need to reintroduce DB transactions around multi-child creation. Per-index idempotent resubmission is the correct recovery mechanism.
- No `swf-go` code changes are assumed in this plan. The only runtime-specific follow-up left is future remote-runtime support if these ops ever need to run through that path.
- Child job IDs should look like the existing KSUID-based identifiers already used elsewhere in the system; the deterministic identity rule changes the bytes behind the ID, not the outward format.
