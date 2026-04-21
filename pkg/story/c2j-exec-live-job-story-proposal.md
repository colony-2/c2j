# Proposal: Use `JobRunStory` as the Live Execution Model for `c2j exec`

## Problem

`c2j exec` and the story API currently observe the same job through two different models:

- `c2j exec` renders live progress directly from worker/replay callbacks in
  [cmd/c2j/internal/runjob/progress.go](/src/cmd/c2j/internal/runjob/progress.go).
- `GetJobRunStory` reconstructs a structured `JobRunStory` by replaying the job in
  [pkg/story/internal/service/service.go](/src/pkg/story/internal/service/service.go)
  via [pkg/story/internal/story/builder.go](/src/pkg/story/internal/story/builder.go).

That split has a cost:

- CLI progress is human-readable but not structured.
- Story data is structured but currently produced through replay/service lookup rather than as the
  primary live model inside `c2j exec`.
- We effectively maintain two execution views of the same job.

The goal of this proposal is to make `JobRunStory` the canonical execution model for `c2j exec`,
while keeping the existing story API and replay behavior.

## Current Baseline

### `c2j exec`

`c2j exec` currently does two things in
[cmd/c2j/internal/runjob/service.go](/src/cmd/c2j/internal/runjob/service.go):

1. Replays cached history with `deps.engine.ReplayJobRun(...)` and a `progressPrinter`.
2. Runs the currently leaseable work with `swf.GetJobForRun(...).Run(...)`, again using
   `progressPrinter` plus `printingExecutor`.

The live display is driven by:

- `swf.ReplayObserver` methods:
  - `OnJobStart`
  - `OnTaskStart`
  - `OnTaskEnd`
  - `OnJobEnd`
- executor callbacks:
  - `ExecuteRecipe`
  - `ExecuteSequence`
  - `ExecuteOp`
  - `ExecuteStateMachine`

That is enough to show progress, but it only prints event lines. It does not maintain a reusable
tree model of the run.

### Story reconstruction

`GetJobRunStory` already builds a recipe-centric tree by replay:

- entrypoint:
  [pkg/story/internal/service/service.go](/src/pkg/story/internal/service/service.go)
- builder:
  [pkg/story/internal/story/builder.go](/src/pkg/story/internal/story/builder.go)
- recorder:
  [pkg/story/internal/story/replay_recorder.go](/src/pkg/story/internal/story/replay_recorder.go)
- execution decoration:
  [pkg/story/internal/story/recording_executor.go](/src/pkg/story/internal/story/recording_executor.go)

This is not strictly "after the fact." The current story builder already supports running jobs:

- replay cache misses are mapped to `running`
- running step nodes can be synthesized when future task output is not yet available

So the story model itself is already capable of representing live execution. What is missing is
using it as the in-process model during `c2j exec`.

## Recommendation

Do **not** make `c2j exec` poll `GetJobRunStory`.

Instead:

1. Extract the reusable live story recorder from `pkg/story/internal/story` into an exported,
   runtime-oriented package.
2. Use that recorder directly inside `c2j exec`.
3. Make the existing replay/service path a thin wrapper around the same recorder.

This keeps one execution model and avoids an extra service hop or repeated replay work during local
execution.

## Proposed Architecture

## 1. Extract a reusable live recorder

Create an exported package, for example:

- `pkg/story/live`

This package should own the logic that is currently split across:

- `replay_recorder.go`
- `recording_executor.go`
- `state_observer.go`
- `recording_helpers.go`
- `tree.go`

The new package should expose something like:

```go
type Recorder struct { ... }

type Options struct {
    JobKey              swf.JobKey
    CELOptionsProvider  template.CELOptionsProvider
    RootSourceResolver  compiler.RecipeSourceResolver
    Logger              *slog.Logger
}

func NewRecorder(opts Options) *Recorder

func (r *Recorder) Observer() swf.ReplayObserver
func (r *Recorder) ExecutorFactory() func() compiler.RecipeExecutor
func (r *Recorder) OnRecipeLoaded(recipeName string)
func (r *Recorder) OnRecipeSourceResolved(resolution compiler.RecipeSourceResolution)

func (r *Recorder) Snapshot() *story.JobRunStory
func (r *Recorder) Finalize(err error) *story.JobRunStory
```

Key point: `Snapshot()` is the missing primitive today.

The current recorder already accumulates enough state incrementally. Right now it mainly exposes
`BuildStory(replayErr error)` at the end. For `c2j exec`, we need mid-run snapshots as tasks and
nodes change state.

## 2. Keep replay-based `BuildJobRunStory`

`BuildJobRunStory(...)` should stay as the public replay convenience API, but internally it should
become:

1. construct `live.Recorder`
2. pass its observer + executor hooks into `compiler.NewRecipeJobWorker(...)`
3. call `engine.ReplayJobRun(...)`
4. return `recorder.Finalize(replayErr)`

That keeps the current API surface while removing the duplicate implementation.

## 3. Use the recorder directly in `c2j exec`

Replace `progressPrinter` as the primary model source in
[cmd/c2j/internal/runjob/service.go](/src/cmd/c2j/internal/runjob/service.go).

At runtime:

1. create a `storylive.Recorder`
2. pass its hooks into the live worker:
   - `OnRecipeLoaded`
   - `OnRecipeSourceResolved`
   - `ExecutorFactory`
3. give its `Observer()` to:
   - cached replay
   - live execution
4. render from `Snapshot()` / `Finalize(...)`

Pseudo-flow:

```go
rec := storylive.NewRecorder(...)

worker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{
    CELOptionsProvider:     deps.celProvider,
    RootSourceResolver:     deps.rootResolver,
    OnRecipeLoaded:         rec.OnRecipeLoaded,
    OnRecipeSourceResolved: rec.OnRecipeSourceResolved,
    ExecutorFactory:        rec.ExecutorFactory(),
})
```

Then:

- cached replay:
  `deps.engine.ReplayJobRun(... Observer: rec.Observer(), JobWorker: worker)`
- live run:
  `runnable.Run(rec.Observer())`

This gives `c2j exec` a single tree that starts with cached history and then continues live.

## 4. Add a CLI renderer on top of `JobRunStory`

The renderer should become a consumer of story snapshots, not the producer of execution state.

Recommended split:

- `pkg/story/live`: state accumulation
- `cmd/c2j/internal/runjob/storyrender.go`: terminal presentation

The renderer can support two modes:

### A. Streaming line mode

Closest to current behavior.

- emits lines only when a node meaningfully changes
- works well in CI and non-TTY environments

Example:

```text
[cached] recipe test
[cached]   sequence main
[cached]     op git.clone
[cached]     done git.clone
[live]   op codex.exec
[live]     step run running
```

### B. TTY tree mode

Preferred interactive UX.

- redraws a compact tree in place
- highlights the active path
- shows retry attempts and running nodes inline

Example shape:

```text
recipe test                          running
  source resolution                  succeeded
  sequence main                      running
    op git.clone                     succeeded
    op codex.exec                    running
      step run                       running
```

The important design point is that both modes render the same `JobRunStory`.

## Why the story model is a good fit

The current model already covers the concepts `c2j exec` needs:

- tree structure:
  recipe / sequence / op / step / state machine / state / transition evaluation
- live statuses:
  `pending`, `running`, `succeeded`, `failed`, `canceled`, `skipped`
- retries:
  `PriorAttempts`
- job restarts:
  `PastAttempts`
- timestamps:
  `StartedAt`, `FinishedAt`
- restart surfaces:
  `TaskOrdinal`, `RestartFromOrdinal`
- root-source-resolution node for non-artifact recipe starts

That is a stronger model than the current line printer. It is also already used for API/UI
consumption, so reusing it in the CLI reduces model drift.

## What changes in behavior

### Today

`c2j exec`:

- prints events as they happen
- does not preserve a stable in-memory story tree
- cannot easily switch renderers or emit machine-readable progress snapshots

### After this change

`c2j exec`:

- maintains a canonical `JobRunStory` in memory
- renders the CLI from that model
- can optionally emit JSON snapshots or structured deltas later without reworking execution

## Proposed implementation phases

## Phase 1: Refactor without changing UX

Goal: keep the current output approximately the same.

Work:

- extract reusable recorder from `pkg/story/internal/story` into `pkg/story/live`
- keep `progressPrinter` only as a renderer fed from story deltas
- make `BuildJobRunStory` use the same recorder internally

Success criteria:

- `c2j exec` output is still readable
- `GetJobRunStory` behavior is preserved
- recorder logic exists only once

## Phase 2: Add snapshot rendering

Work:

- implement `Snapshot()` and stable node IDs
- add a tree renderer in `cmd/c2j/internal/runjob`
- choose redraw policy:
  - on every observer/executor event for TTY
  - line-only in CI

Success criteria:

- interactive `c2j exec` can show a live tree view
- cached replay and live work appear in one continuous story

## Phase 3: Optional structured output

Work:

- add `--progress-format=story-json|events|tree`
- optionally emit full snapshots or deltas

Success criteria:

- external tools can consume the same live story model
- no extra execution path is introduced

## Package boundary recommendation

This is the main structural change:

- keep model types in `pkg/story`
- move live recorder logic to exported `pkg/story/live`
- keep service-only wrapper logic in `pkg/story/internal/service`

Why:

- `cmd/c2j` cannot import `pkg/story/internal/story`
- the logic is no longer purely internal once `c2j exec` needs it
- exported runtime recorder is a cleaner seam than making CLI code reach into an `internal` tree

## Testing strategy

## 1. Preserve existing story replay tests

Current tests in [pkg/story/internal/story/replay_story_test.go](/src/pkg/story/internal/story/replay_story_test.go)
should continue to validate the final tree semantics.

## 2. Add live-recorder tests

New tests should verify:

- `Snapshot()` after `OnJobStart`
- `Snapshot()` after `OnTaskStart`
- `Snapshot()` after a successful `OnTaskEnd`
- `Snapshot()` after replay cache miss
- retry behavior reflected in `PriorAttempts`
- cached replay followed by live execution yields one coherent tree

## 3. Add `c2j exec` renderer tests

Validate:

- line mode output from story deltas
- tree mode output for a small recipe with:
  - sequence
  - op
  - state machine
  - retry

## Open questions

## 1. Snapshot frequency

Should the CLI redraw:

- on every observer/executor event
- on a small debounce window
- only when the visible tree actually changes

Recommendation:

- event-driven with a short debounce for TTY
- immediate line emission for CI

## 2. Full snapshots vs deltas

The model is a tree, but the renderer may not need the full tree each time.

Recommendation:

- implement `Snapshot()` first
- compute renderer-local diffs later if redraw cost becomes a problem

## 3. Cached replay presentation

Should cached history appear the same as live events?

Recommendation:

- keep a visual distinction (`[cached]` vs `[live]`) in line mode
- collapse cached-complete branches by default in TTY mode, with an option to expand later

## Practical recommendation

The best next step is:

1. extract the recorder from `pkg/story/internal/story` into `pkg/story/live`
2. add `Snapshot()` / `Finalize(...)`
3. rewire `c2j exec` to use the recorder as its execution model
4. keep the current terminal UX initially, but render it from story deltas instead of direct event
   printing

That gives us one shared model for:

- API/UI inspection via replay
- local live execution in `c2j exec`
- future structured progress output

without introducing a second observability system.
