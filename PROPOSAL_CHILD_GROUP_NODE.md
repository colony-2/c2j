# Proposal: Child Group Node

Status: draft proposal

## Summary

Add `child_group` as a first-class recipe authoring node for durable child
recipe fan-out and fan-in.

The important implementation constraint is that child job starts are side
effects. In the current recipe runtime, side effects are durable because they
happen inside SWF task-backed ops. A `child_group` node should therefore be a
first-class syntax and story concept, but its start, await, collect, and
aggregate work should lower to internal task-backed recipe ops.

Recommended shape:

```yaml
sequence:
  - id: counterpoint
    child_group:
      mode: run_and_get_result
      children:
        - key: requirements
          recipe: ticket-review-requirements
          required: true
        - key: compatibility
          recipe: ticket-review-implementation-compat
          required: true
        - key: outcome
          recipe: ticket-review-outcome
          required: false
      artifacts:
        use:
          - "${{ context.artifacts['ticket-intake'] }}"
      aggregate:
        shape: review_pack
        artifact: reviews/review-pack.json
```

Downstream recipe references stay in the existing shape:

```yaml
outputs:
  ok: "${{ sequence.counterpoint.outputs.ok }}"
  child_job_ids: "${{ sequence.counterpoint.outputs.child_job_ids }}"
  blocking_issues: "${{ sequence.counterpoint.outputs.blocking_issues }}"
```

## Why This Fits The Current Recipe Model

Recipes already have a small node union:

- `op`
- `sequence`
- `state`
- `shared`

`op` nodes are the side-effect boundary. The compiler resolves inputs, calls
`ctx.DoTask`, and records task output in the normal `sequence.<id>.outputs` or
`states.<id>.outputs` container.

Existing child recipe behavior also lives behind ops:

- `recipe.run_and_get_result`
- `recipes.run_and_wait`
- `recipes.run`
- `recipe.await_result`
- `recipe.get_result`

Those ops already know how to:

- start recipe jobs;
- pass recipe source and git context;
- pass artifact refs;
- use deterministic child job IDs;
- use `AwaitJobs` for durable parent reschedule.

The proposed `child_group` should reuse that machinery rather than introducing
a new execution loop that starts jobs directly from a composite node. Direct
starts from the recipe executor would be replay-unsafe because SWF replay
would execute the start side effect again instead of reading a cached task
result.

## Recommended Architecture

Use a two-layer design.

### 1. User-facing node

Add a `child_group` node to the recipe AST:

```go
type NodeChildGroup struct {
    NodeMetadata  `yaml:",inline"`
    ChildGroup    ChildGroupData `yaml:"child_group"`
}
```

Add the equivalent root recipe type only if needed for completeness:

```go
type RecipeChildGroup struct {
    RecipeMetadata `yaml:",inline"`
    ChildGroup     ChildGroupData `yaml:"child_group"`
}
```

The child group configuration is nested under `child_group:`. This avoids
colliding with existing top-level `artifacts:` node metadata, which already
means op inbox bindings.

### 2. Durable internal execution

The compiler should render the child group into a fully resolved invocation
payload, then execute one of two internal ops:

- `recipe.child_group.start`
- `recipe.child_group.run_and_get_result`

These ops can be registered like normal recipe ops but treated as internal
implementation details. Recipe authors should use `child_group:` syntax, not
author those op names directly.

This keeps:

- task output caching;
- deterministic replay;
- retry and timeout behavior;
- normal output/artifact recording;
- existing op dependency access to `WorkflowControl`, `JobTool`, git context,
  and artifact helpers.

### 3. Reuse existing child-op steps where the types fit

The existing op framework already supports multi-step ops. That is the right
runtime shape for `child_group`:

```go
ops.NewOp().
    WithType("recipe.child_group.run_and_get_result").
    AddStep("start", ...).
    AddStep("await", ...).
    AddStep("collect", ...).
    BuildOrPanic()
```

This should reuse the existing child recipe op implementation at the function
level:

- `startJobs` / `recipeToStart` for starting recipe jobs;
- the deterministic child job ID machinery, extended to accept a stable child
  key when available;
- `deps.JobTool().AwaitJobs(...)` for the await step;
- existing artifact-ref forwarding behavior.

It should not literally compose the public `recipes.run` or
`recipe.run_and_get_result` ops as black boxes. Their public step output types
are too narrow:

- `recipes.run` returns only `job_ids`;
- `recipe.run_and_get_result` handles one child and treats child failure as a
  hard op error;
- neither carries child keys, required flags, skipped records, aggregate config,
  or per-child soft failures between steps.

So `child_group` should have its own internal step-state structs while calling
the same lower-level child launch and await helpers. That keeps the durable
multi-step SWF behavior and avoids duplicating the already-correct child start
plumbing.

## Authoring Surface

### Static Children

```yaml
id: counterpoint
child_group:
  mode: run_and_get_result
  children:
    - key: requirements
      recipe: ticket-review-requirements
      cell_name: recipe-tests
      required: true
      inputs:
        prompt: "${{ inputs.prompt }}"
    - key: outcome
      recipe: ticket-review-outcome
      required: false
```

### Dynamic Children

```yaml
id: spawn_dependency_jobs
child_group:
  mode: start
  children_from: "${{ json_parse(states.implementation.outputs.plan_json).dependency_job_specs }}"
  child:
    key: "${{ item.id }}"
    recipe: new-ticket
    cell_name: "${{ item.target_cell }}"
    required: true
    inputs:
      prompt: "${{ item.scope }}"
  aggregate:
    shape: job_ids
```

`children_from` is not a general loop. It is a list expander for child recipe
specs only.

The compiler should resolve `children_from` first, then render `child` once per
item with these local variables:

- `item`: current list element;
- `index`: zero-based item index.

Those locals are available only while rendering the child template. This keeps
the feature narrow and avoids adding arbitrary repeated computation to recipes.

### Child Spec Fields

Initial child recipe spec:

```yaml
key: requirements
recipe: ticket-review-requirements
cell_name: recipe-tests
required: true
when: "${{ inputs.enable_requirements_review }}"
skip_reason: requirements review disabled
inputs: {}
artifacts: []
git_ref: "${{ context.git.hash != '' ? context.git.hash : context.git.ref }}"
```

Rules:

- `recipe` maps to the existing child op `name` field internally.
- `required` defaults to `true`.
- `key` should be required for dynamic children and strongly encouraged for
  static children.
- If `key` is omitted, c2j may use the string form of the child index, but
  diagnostics should warn because index keys are poor test and story targets.
- Duplicate keys in one group are a validation error.
- `when` is evaluated per child after the child spec is rendered.
- A false `when` produces a skipped child record. It does not start a child
  job and does not make the group fail.

## Execution Modes

Only two modes should be supported initially.

### `start`

Start every non-skipped child and return handles.

No child result fetching happens in this mode. Outputs still include child
records, summary counts, and job ID lists.

### `run_and_get_result`

Start every non-skipped child, wait until all started children are terminal,
then collect every child result.

The wait should use the existing SWF `AwaitJobs` primitive. This gives durable
wait-for-all behavior without polling and without inventing wait-for-any,
streaming, race, or fail-fast semantics.

Child recipe failures are soft data after all children finish. The parent
recipe should continue to aggregate and route using `outputs.ok`,
`outputs.summary`, `outputs.blocking_issues`, and `outputs.warnings`.

## Soft Result Collection

The current `recipe.get_result` path calls `JobResult`, which intentionally
returns an error for failed child jobs. `child_group` needs a lower-level result
reader so failed children can be represented as data.

Add a method to `workflowctl.WorkflowControl`, for example:

```go
GetJobRun(ctx context.Context, key swf.JobKey, opts JobRunOptions) (swf.GetJobRunResponse, error)
```

or a narrower c2j result helper:

```go
ChildJobResult(ctx context.Context, key swf.JobKey) (ChildJobRunResult, error)
```

The implementation should use `swf.GetJobRun(... IncludeOutputs: true,
IncludeArtifacts: true)` and inspect the latest attempt. It should not call
`GetOutput` as the primary path because `GetOutput` converts failed outcomes
into hard errors.

Distinguish these failures:

- child spec rendering failure: hard recipe execution error before starting;
- child start failure: child record failure when attributable to one child
  spec, hard task error for systemic workflow-control failures;
- child await failure: hard task error because the durable wait primitive
  failed;
- child recipe failure: child record failure, then required/optional handling;
- aggregate rendering failure: hard task error because the group output cannot
  be produced deterministically.

## Output Shape

The group output should be stable across all modes:

```json
{
  "ok": false,
  "mode": "run_and_get_result",
  "child_job_ids": ["job-1", "job-2"],
  "required_child_job_ids": ["job-1"],
  "optional_child_job_ids": ["job-2"],
  "failed_child_job_ids": ["job-1"],
  "children": [
    {
      "key": "requirements",
      "index": 0,
      "recipe": "ticket-review-requirements",
      "cell_name": "recipe-tests",
      "required": true,
      "skipped": false,
      "skip_reason": "",
      "job_id": "job-1",
      "status": "failed",
      "outputs": {},
      "artifacts": {},
      "failure": {
        "kind": "child_recipe_failed",
        "message": "command exited 1"
      }
    }
  ],
  "summary": {
    "total": 2,
    "started": 2,
    "completed": 1,
    "failed_required": 1,
    "failed_optional": 0,
    "skipped": 0
  },
  "aggregate": {},
  "warnings": [],
  "blocking_issues": []
}
```

Recommended status values:

- `started`
- `completed`
- `failed`
- `cancelled`
- `skipped`
- `start_failed`

For `start` mode, successfully started children use `started`. For
`run_and_get_result`, terminal children use `completed`, `failed`, or
`cancelled`.

`ok` should mean "the group can proceed according to its aggregate policy":

- for `none` and `job_ids`, all required children must start and complete
  without child recipe failure;
- for `review_pack`, required reviewer failures and required reviewer blocking
  issues make `ok=false`;
- optional child failures produce warnings by default.

## Aggregation Profiles

Do not add custom aggregation expressions in the first version. Named profiles
fit the current recipe style better because they keep the node deterministic,
schemaable, and testable.

### `none`

No semantic aggregate. The output still includes standard child records,
summary counts, and job ID lists.

### `job_ids`

Designed for dependency fan-out.

```json
{
  "aggregate": {
    "jobs": [
      {
        "key": "REQ-1",
        "target_cell": "frontend",
        "job_id": "job-123",
        "required": true
      }
    ]
  },
  "child_job_ids": ["job-123"]
}
```

### `review_pack`

Designed for reviewer recipes that use a small conventional output contract:

```json
{
  "ok": false,
  "blocking_issues": [],
  "warnings": [],
  "artifact_refs": {}
}
```

The aggregate should produce:

```json
{
  "aggregate": {
    "ok": false,
    "reviewers": [],
    "blocking_issues": [],
    "warnings": []
  },
  "blocking_issues": [],
  "warnings": []
}
```

Reviewer entries should include:

- child key;
- recipe;
- cell;
- required flag;
- status;
- child `outputs.ok` when present;
- blocking issues;
- warnings;
- artifact refs.

If a required reviewer fails before producing outputs, add a blocking issue
describing that failure. If an optional reviewer fails before producing outputs,
add a warning.

## Artifacts

Use two artifact concepts.

### Child Input Artifacts

`child_group.artifacts.use` defines artifact refs passed to every child unless
a child overrides or extends them.

Recommended first-version behavior:

- entries resolve to artifact refs;
- bare strings may be accepted as shorthand for `context.artifacts[name]`;
- dynamic expressions should be preferred in docs because they are explicit.

Example:

```yaml
child_group:
  artifacts:
    use:
      - "${{ context.artifacts['ticket-intake'] }}"
```

### Child Output Artifacts

Preserve child artifacts in each child record:

```json
{
  "children": [
    {
      "key": "requirements",
      "artifacts": {
        "review.md": { "stored": { "...": "..." } }
      }
    }
  ]
}
```

The parent node's artifact map should avoid name collisions. Recommended
materialized names:

```text
children/<child-key>/<child-artifact-name>
```

The aggregate artifact, if configured, should use the authored path:

```yaml
aggregate:
  shape: review_pack
  artifact: reviews/review-pack.json
```

The artifact content should match `outputs.aggregate`. The node output should
also include an aggregate artifact ref, for example:

```json
{
  "aggregate_artifact": {
    "name": "reviews/review-pack.json"
  }
}
```

## Deterministic Child Job IDs

Existing child recipe ops derive deterministic IDs from parent job ID,
invocation identity, and recipe index.

`child_group` should derive IDs from:

- parent tenant and job ID;
- group invocation path and sequence;
- child key when present;
- child index only as fallback.

Using the child key gives stable IDs when a dynamic list is regenerated in the
same order and gives clearer conflict diagnostics when the same key resolves to
different child specs on replay.

Duplicate explicit child keys should fail before any start task runs.

## Recipe Testing

Extend the newer `pkg/recipetest` case schema with child-group mocks rather
than forcing tests to mock the internal ops.

Suggested shape:

```yaml
mocks:
  child_groups:
    - match:
        node_path: root.counterpoint
      children:
        requirements:
          job_id: job-requirements
          status: completed
          outputs:
            ok: true
          artifacts: {}
        outcome:
          job_id: job-outcome
          status: failed
          required: false
          failure:
            message: optional review failed
```

Testing rules:

- mocks are addressed by child key;
- a mock can cover start-only mode by providing job IDs only;
- a mock can cover result mode by providing status, outputs, artifacts, and
  failure;
- aggregate output may be either computed from mocked children or explicitly
  overridden for focused tests;
- passthrough tests can keep using secondary recipe fixtures.

The older fixture framework should continue to support real secondary recipes
through the existing `recipes:` list.

## Job Story

Add a story node kind:

```go
JobRunStoryNodeKindChildGroup = "childGroup"
```

The story node should show:

- group ID and mode;
- aggregate profile and artifact path;
- total, started, skipped, failed required, and failed optional counts;
- each child key, recipe, cell, required flag, skip status, job ID, status, and
  failure summary;
- whether child outputs and artifacts were collected.

Child recipe jobs should remain separate job stories linked by job ID. The
parent story should not inline the full child job tree in the first version.

Internally, if execution lowers to `recipe.child_group.*` ops, the story
recorder can hide or collapse those implementation op nodes under the
`childGroup` node.

## Implementation Outline

### Recipe AST and Schema

Update:

- `pkg/recipe/node.go`
- `pkg/recipe/recipe.go`
- `pkg/recipe/schema.go`
- `pkg/recipe/visitor.go`

Add:

- child group structs;
- YAML unmarshal support for `child_group`;
- schema oneOf entries;
- visitor traversal behavior;
- validation placeholder support so `sequence.<id>.outputs.ok` and standard
  child-group output fields validate.

### Template Rendering

Add a small local-variable rendering helper to `pkg/template`, for example:

```go
ResolveValueWithLocals(value any, locals map[string]any) (any, error)
```

Use it only for `children_from` child template rendering initially. The locals
should be merged into CEL variables as `item` and `index`, not into `vars`, so
authored expressions match the proposed syntax.

### Compiler

Extend `RecipeExecutor` with `ExecuteChildGroup`.

`DefaultRecipeExecutor.ExecuteChildGroup` should:

1. create an op-like rendering context from node metadata;
2. resolve group vars;
3. render static or dynamic child specs;
4. evaluate per-child `when`;
5. normalize skipped children;
6. build an internal child-group invocation payload;
7. execute the selected internal op through existing op execution machinery;
8. let the internal op record outputs and artifacts under the group ID in the
   parent resolution context.

The method should not call `WorkflowControl.StartJob` directly.

The implementation does not need to expose a new top-level template container.
Downstream authors should still read the result through the existing
`sequence.<id>.outputs` or `states.<id>.outputs` maps.

### Internal Ops

Add an internal package or extend `pkg/ops/recipe` with:

- `recipe.child_group.start`
- `recipe.child_group.run_and_get_result`

The `run_and_get_result` op should be a normal multi-step op with task-backed
phases:

1. `start`: start all non-skipped children, preserving per-child start
   failures where possible;
2. `await`: call `deps.JobTool().AwaitJobs(jobIDs...)`;
3. `collect`: fetch terminal child run details, build child records, aggregate,
   and attach artifacts.

The `start` op can share the same start step implementation and stop there.

The step chain should use child-group-specific structs such as:

```go
type ChildGroupStartInput struct {
    Mode      string
    Children  []RenderedChildSpec
    Aggregate ChildGroupAggregateSpec
}

type ChildGroupStepState struct {
    Mode      string
    Children  []ChildRecord
    Aggregate ChildGroupAggregateSpec
    JobIDs    []string
}
```

That state can include everything the later `await` and `collect` steps need,
while the start implementation still delegates to the existing `startJobs`
helper for actual submission.

### Workflow Control

Extend `workflowctl.WorkflowControl` and `pkg/worker/workflow/control.go` with
a child-result detail method based on `swf.GetJobRun`.

Keep existing `JobResult` behavior unchanged for current ops.

### Story

Update:

- `pkg/story/internal/model`
- `pkg/story/live/recorder.go`
- `cmd/c2j/internal/runjob/story_progress.go`

Add a child-group story node and render child job references compactly.

## Rollout Plan

1. Add the internal child-group op types and unit-test their output assembly
   using fake workflow control.
2. Add `WorkflowControl` child result details without changing existing
   `recipe.get_result` behavior.
3. Add the recipe AST node and parser/schema support.
4. Add compiler rendering and lowering to internal ops.
5. Add story rendering.
6. Add recipe-test child-group mocks.
7. Add fixture recipes for:
   - static start mode;
   - dynamic start mode;
   - wait and collect success;
   - required child failure;
   - optional child failure warning;
   - skipped child;
   - `review_pack` aggregate artifact.

## Rejected Alternatives

### Implement only a public op

This is easy to wire into the current runtime, but it handles dynamic children
poorly. The normal op input resolver would try to resolve `item.id` before
there is an `item`. A node-level renderer is the cleaner place to expand
`children_from`.

### Start jobs directly from a composite node

This gives a nice AST but breaks the runtime model. Child starts would happen
outside `ctx.DoTask`, so replay would not have a cached start result to read.
Deterministic IDs reduce duplicates, but they do not make direct side effects
the right abstraction.

### Add a general loop node

The request is about durable child recipe orchestration, not arbitrary repeated
computation. A general loop would be broader, harder to validate, harder to
render in stories, and unnecessary for the stated workflows.

## Open Questions

- Should bare strings in `artifacts.use` be accepted as shorthand for submitted
  artifact names, or should v1 require explicit artifact ref expressions?
- Should a child start failure always be soft data, or should unknown recipe
  resolution remain a hard task error?
- Should child output artifacts always be exposed under
  `children/<key>/...`, or should that be opt-in to avoid increasing parent
  artifact lists?
