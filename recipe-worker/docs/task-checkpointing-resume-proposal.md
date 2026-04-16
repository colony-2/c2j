# Proposal: Task Checkpointing + TaskWorker Resume

**Date:** 2026-03-04  
**Status:** Brainstorm / design draft  
**Audience:** swf-go, recipe-core, recipe-worker, recipe-template, ops teams

## 1) Why this change

We want long-running tasks to save durable intermediate progress (output + artifacts) and continue later without redoing completed work.

The requested target is:

1. A new SWF task checkpoint API (save intermediate output/artifacts).
2. A new worker resume API (`Resume(input, checkpoint0...checkpointN)`).
3. Recipe runtime/compiler/ops APIs updated so ops can use this safely.

## 2) Current baseline (important constraints)

Today in this workspace:

1. `TaskWorker` only has `Run(ctx, input)` (no resume hook).
2. `recipe-worker` task outputs are wrapped in `task.OutputEnvelope` (`activity_output`, `context_patch`).
3. Compiler already handles replay-time mismatch (`TaskInputMismatchError`) and can apply `context_patch` then retry.
4. SWF restart already supports chapter injection (`ExtraTaskInput`/`ExtraTaskOutput`) and replay.
5. Op execution is step-based (`TaskStep`) and assumes one terminal task output per step.

This means checkpoint/resume can be introduced without rewriting the full recipe model, but we need new contracts around determinism and chapter handling.

## 3) Recommended architecture

### 3.1 SWF API shape

Use an additive API that keeps old workers valid.

```go
// Existing interface stays valid.
type TaskWorker interface {
    Name() string
    Run(ctx TaskContext, input TaskData) (TaskData, error)
}

// Optional extension.
type CheckpointResumableTaskWorker interface {
    TaskWorker
    Resume(ctx TaskContext, input TaskData, checkpoints []TaskCheckpoint) (TaskData, error)
}

type TaskCheckpoint struct {
    Seq        int64           // monotonic within a task attempt
    Name       string          // optional logical name
    CreatedAt  time.Time
    InputHash  string          // same hash used for deterministic replay
    Data       TaskData        // checkpoint payload
    SourceTask string          // task type
}
```

Add a checkpoint API on `TaskContext`:

```go
func (tc TaskContext) Checkpoint(data TaskData, opts ...CheckpointOption) error
```

### 3.2 SWF runtime behavior

For each task attempt:

1. Runner first scans chapters at/after the task ordinal.
2. If a terminal task outcome chapter exists and input hash matches, return it (existing behavior).
3. If checkpoint chapters exist and no terminal outcome exists:
   1. If worker implements `CheckpointResumableTaskWorker`, call `Resume(ctx, input, checkpoints)`.
   2. Otherwise fail with deterministic error (checkpoint exists but worker cannot resume).
4. `TaskContext.Checkpoint(...)` appends a `TaskCheckpoint` chapter (write-once ordinal model still applies).
5. Final `Run`/`Resume` return writes the normal terminal task outcome chapter.

### 3.3 Determinism contract

1. Every checkpoint chapter stores task input hash.
2. Resume only uses checkpoints whose input hash matches current computed hash.
3. Hash mismatch remains a determinism error (same posture as today).
4. Checkpoint sequence (`Seq`) is immutable and monotonic.

## 4) Ops API enhancements

To make this usable by op implementations, expose checkpoint tools through `OpDependencies`.

## 4.1 `recipe-core/pkg/ops` additions

Extend `OpDependencies`:

```go
type OpDependencies interface {
    // existing methods...
    Checkpointer() TaskCheckpointer
    ResumeInfo() TaskResumeInfo
}

type TaskCheckpointer interface {
    Save(name string, payload any, artifacts ...swf.Artifact) error
    LoadAll() []TaskCheckpointMeta
}

type TaskResumeInfo struct {
    IsResume    bool
    Checkpoints []TaskCheckpointMeta
}
```

This lets long-running ops (for example `codex.exec` style ops) checkpoint explicit progress without building SWF-specific plumbing themselves.

### 4.2 `workflowctl` additions

For operational visibility and external tooling:

1. Add read API for task checkpoints (by job/task run).
2. Add restart-from-checkpoint helper API (job-level orchestration convenience, built on SWF restart).

This keeps checkpoint inspection out of ad-hoc story chapter parsing in higher layers.

## 5) Recipe-worker changes

### 5.1 `pkg/ops/taskworker.go`

Implement optional `Resume(...)`:

1. `Run` remains unchanged for first execution.
2. `Resume` reconstructs invocation state from checkpoint list and calls `opExecutor` with resume context.
3. Terminal result still returns `OutputKindActivityInvocationOutput` envelope.

### 5.2 `pkg/ops/op_executor.go`

Split execution into resumable phases and checkpoint boundaries:

1. Restore git/worktree + materialize inbox.
2. Execute op logic.
3. Persist git diffs + collect outbox artifacts.
4. Build activity output envelope.

At phase boundaries, optional checkpoint writes can include:

1. Partial structured output.
2. Artifact keys emitted so far.
3. Git context snapshot.

### 5.3 Backward compatibility

If an op never checkpoints:

1. No new chapters are written.
2. Worker behavior is identical to today.

## 6) Compiler adjustments

Two levels:

### 6.1 Level A (minimal, recommended first)

No control-flow changes in compiler.

Reason: DoTask still returns one terminal task output; checkpoint/replay is handled below compiler in SWF runtime and task worker resume path.

Compiler work in this level:

1. Add tracing/logging fields for resume/checkpoint count in `executeOp2`.
2. Keep current context-patch replay loop as-is.

### 6.2 Level B (optional, recipe-visible checkpoint events)

If we want recipe logic to react directly to checkpoint events (not just final output), add a new output kind:

1. `task_checkpoint_event` in `task.OutputEnvelope`.
2. Compiler branch in `executeOp2` to:
   1. apply patch/event payload,
   2. refresh resolved inputs/artifacts,
   3. re-issue DoTask.

This is similar to existing `context_patch` handling but with explicit checkpoint semantics.

## 7) Artifact and git semantics

To avoid data loss/duplication:

1. Checkpoint artifacts should be persisted as normal SWF artifacts and referenced by key.
2. Resume should rehydrate by artifact key, not by embedding blobs in checkpoint payload.
3. Git thin-pack handling must remain pass-through compatible:
   1. if checkpoint includes updated git state, use that state as resume baseline;
   2. otherwise continue current restore/persist behavior.

## 8) Migration plan

### Phase 1: SWF primitives

1. Add checkpoint chapter type + `TaskContext.Checkpoint`.
2. Add optional `CheckpointResumableTaskWorker`.
3. Add runner logic to call `Resume` when appropriate.

### Phase 2: recipe-core API surfaces

1. Add `OpDependencies.Checkpointer()` and `ResumeInfo()`.
2. Add checkpoint inspection APIs in `workflowctl`.

### Phase 3: recipe-worker integration

1. Add `taskWorker.Resume`.
2. Add checkpoint-aware `opExecutor` helpers.
3. Keep default behavior unchanged for non-checkpointing ops.

### Phase 4: compiler enhancements (only if needed)

1. Add recipe-visible checkpoint output kind and compiler handling.
2. Reduce reliance on mismatch-injected patch tricks where possible.

## 9) Testing plan

1. **swf-go**
   1. checkpoint write/read ordering.
   2. resume with N checkpoints.
   3. hash mismatch determinism errors.
   4. legacy worker compatibility (no resume method).
2. **recipe-worker**
   1. op runs with no checkpoints unchanged.
   2. op checkpoints and resumes mid-task successfully.
   3. artifact rehydration across resume.
   4. git thin-pack correctness after resume.
3. **compiler**
   1. no regression in context_patch replay.
   2. optional checkpoint-event flow (if Phase 4 adopted).

## 10) Open questions to settle before implementation

1. Should checkpoints be invisible to recipe control flow (Level A only), or first-class events in compiler (Level B)?
2. Do we require exact checkpoint sequence determinism, or only final-output determinism?
3. Should `Resume` receive all checkpoints or only the latest per checkpoint name?
4. Do we allow checkpoint API for every task, or gate it behind capability flags per task worker?
5. How much checkpoint metadata should be exposed in job run APIs by default (noise vs observability)?

## 11) Practical recommendation

Start with **Level A**:

1. Add SWF checkpoint+resume primitives.
2. Expose them through ops dependencies.
3. Keep compiler control flow unchanged initially.

This delivers real resumability fast, preserves existing recipe semantics, and minimizes regression risk. Then decide if Level B (recipe-visible checkpoint events) is needed after first consumer ops ship.
