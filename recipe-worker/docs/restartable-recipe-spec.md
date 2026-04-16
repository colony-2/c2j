# Restartable Recipe Jobs with Context Patching

**Date:** 2026-01-29  
**Status:** Proposal  
**Owner:** recipe-worker / recipe-template / recipe-core teams  
**Goal:** allow restarting a recipe run at a specific step while optionally mutating job or scoped context before resuming.

## Scope by Moon Project

- **recipe-worker**
  - Persist task outputs as a typed envelope instead of bare payloads.
  - During replay, detect cached context patches, apply them, then re-run the task with re-resolved inputs.
  - Treat cached activity outputs on mismatch as fatal non-determinism.

- **recipe-template**
  - Add `ResolutionContext.ApplyContextPatch` to mutate job-level context and scoped outputs/artifacts with correct visibility rules.
  - Ensure patched job context is shared to siblings/parents; scoped patches remain local to the addressed scope.

- **recipe-core**
  - Define shared envelope and context patch types.
  - Expose `RestartRecipeJob` API that restarts at a given step offset with an optional context patch.

## Design

### Envelope
- New type `Envelope{ Version, Kind, Payload }`.
- `Kind` enum: `activity_output`, `context_patch` (future-safe for more kinds).
- Shared between worker and compiler; live in `server/recipe-core/pkg/task` (or adjacent common pkg).
- Existing `ActivityInvocationOutput` migrates into the shared package; envelope wraps it for storage in `swf.TaskData`.

### Task Output Recording (recipe-worker)
- `pkg/ops/taskworker.go`:
  - After running an activity, wrap `ActivityInvocationOutput` in `Envelope{Kind: activity_output}` before `swf.NewTaskData`.
  - Keep artifacts unchanged.

### Replay Handling (compiler)
- `pkg/compiler/compiler.go::executeOp2`:
  - Decode task results as `Envelope`.
  - Normal path: `Kind==activity_output` → use existing flow (git update, normalize output, handle `NextTask`).
  - On `swf.TaskInputMismatchError`:
    - If cached payload is `activity_output`: return the error (fatal non-determinism).
    - If cached payload is `context_patch`: apply patch (see below), re-resolve inputs/artifacts with updated context, re-run `DoTask`. Loop to allow multiple sequential patches.

### Context Patch Semantics
- `ContextPatch` supports:
  - **Job context merge**: partial `contextual.JobContext` (e.g., set `context.workflow.author = "james dolan"`). Propagates to all scopes.
  - **Scoped patch**: list of targets, each with scope type/id (sequence/state/op) and optional outputs/artifacts map. Mutates the stored `StepOutput` for that scope only. Updates `lastExecution/lastArtifacts` when current scope is patched.
- JSON shape is deterministic (no unknown fields) to keep SWF task caching stable.

### Template Resolver (recipe-template)
- Add `ResolutionContext.ApplyContextPatch(p ContextPatch)`:
  - Merge job context into a shared pointer so siblings/parents see updates.
  - Locate target scope containers (sequence/state/op) and patch their `StepOutput.Outputs` / `Artifacts`.
  - Avoid leaking scoped patches to unrelated scopes; job patches are globally visible.
- Provide helpers to rebuild `TaskExecutionContext` for current scope after a job-level patch to refresh CEL/template visibility.

### Restart API (recipe-core)
- Add `RestartRecipeJob(ctx, jobKey, stepOffset, patch *ContextPatch)` in `pkg/starter/start_recipe.go` (or helper under `workflowctl`):
  - Build an envelope with `Kind: context_patch`; payload nil when no patch supplied.
  - Call `swf-go` restart with the step offset and envelope, preserving run policy and artifacts.
  - Keep `StartRecipeJob` intact for existing callers.

## Testing
- **recipe-worker**: replay test that injects `TaskInputMismatchError` containing a `context_patch` envelope; verify inputs are re-resolved and task reruns; confirm fatal path for cached `activity_output`.
- **recipe-template**: unit tests for job-context merge visibility (outer/sibling) and scoped patch isolation (only target scope changes); artifacts patching covered.
- **recipe-core**: starter restart API test with fake SWF engine asserting restart call contains envelope and offset.

## Migration / Compatibility
- Envelope versioning allows future message kinds.
- Existing runs remain compatible because cached data is still readable as `activity_output` envelopes.
- Determinism: same input hash still required; patched runs intentionally diverge via restart entry point.

