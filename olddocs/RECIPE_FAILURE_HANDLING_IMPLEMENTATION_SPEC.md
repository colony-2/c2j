# Recipe Failure Handling Catch Implementation Spec

Status: draft

Source proposal: `RECIPE_FAILURE_HANDLING_PATTERN.md`

## Goal

Implement recipe-level `catch:` handlers that can turn selected runtime node
failures into explicit workflow paths.

The implementation should preserve the current success path behavior:

- successful ops, sequences, and state-machine states still use ordinary
  outputs and `transitions`;
- authoring and validation errors still fail fast;
- uncaught runtime failures still propagate and fail the job as they do today.

## Initial Scope

Implement the proposal in two phases.

Phase 1 should cover the current recipe need:

1. Parse and schema-generate `catch:` on every `NodeMetadata`.
2. Evaluate `catch:` for state-machine states.
3. Evaluate fallback `catch:` on state-machine container nodes and recipe-root
   state machines.
4. Support `when`, `to`, `payload`, `continue.outputs`, and `fail`.
5. Add `failure` to CEL and template scope while catch conditions and actions
   are rendered.
6. Add `transition.failure` for catch-driven state transitions.
7. Normalize runtime errors into a stable `failure` map.
8. Ensure catch decisions happen before recipe-level retry scheduling.

Phase 2 can add sequence-step `continue:` and broader op/sequence catch behavior
outside state machines. The data model should support it from the start, but
the first runtime path should be state-machine-first.

## Current Architecture

The implementation touches these existing areas:

- Recipe parsing lives under `pkg/recipe`. `NodeMetadata` is embedded in
  recipe roots and concrete node types. `State` decodes `transitions` specially
  and leaves all other keys to `Node`, so a `catch:` key on a state can live in
  `NodeMetadata`.
- Template and CEL scope lives in `pkg/template.ResolutionContext`. CEL
  variables are registered in `newResolutionContext`; Go-template roots are in
  `template_interpolate.go`.
- State-machine execution lives in
  `pkg/worker/compiler/statemachine_compiler.go`. `runState` wraps a state node
  in a state-scoped resolution context, and `evaluateTransitionsWithContext`
  renders success transitions.
- Op execution lives in `pkg/worker/compiler/compiler.go::executeOp2`.
  It currently passes `metadata.Retry` directly into `swf.RunPolicy`, so SWF
  handles task retries before the compiler receives an error.
- Composite retry is currently not implemented:
  `executeCompositeInEnvelope` accepts `retry` but ignores it.

That retry model is the main impedance mismatch. The proposal requires catch to
see each failed attempt before the next recipe retry is scheduled. If SWF owns
all op retry attempts, c2j only sees the final exhausted error and cannot route
known failures immediately.

## Recipe Model

Add `Catch []CatchClause` to `pkg/recipe.NodeMetadata`.

Suggested structs:

```go
type CatchClause struct {
    ID       string                 `yaml:"id,omitempty" json:"id,omitempty"`
    When     cel.CELExpr            `yaml:"when,omitempty" json:"when,omitempty"`
    To       string                 `yaml:"to,omitempty" json:"to,omitempty"`
    Payload  map[string]interface{} `yaml:"payload,omitempty" json:"payload,omitempty"`
    Continue *CatchContinue         `yaml:"continue,omitempty" json:"continue,omitempty"`
    Fail     *CatchFail             `yaml:"fail,omitempty" json:"fail,omitempty"`
}

type CatchContinue struct {
    Outputs map[string]interface{} `yaml:"outputs,omitempty" json:"outputs,omitempty"`
}

type CatchFail struct {
    Kind    string                 `yaml:"kind,omitempty" json:"kind,omitempty"`
    Code    string                 `yaml:"code,omitempty" json:"code,omitempty"`
    Message string                 `yaml:"message,omitempty" json:"message,omitempty"`
    Cause   interface{}            `yaml:"cause,omitempty" json:"cause,omitempty"`
}
```

Parsing requirements:

- `catch:` must be a list.
- `when:` uses the same normalization rules as transitions: omitted or `true`
  means always match; string values are CEL; boolean values are accepted.
- Exactly one action must be present: `to`, `continue`, or `fail`.
- `payload` is valid only with `to`.
- Empty `id` is allowed but diagnostics should prefer it when present.
- Unknown action combinations should fail validation before execution.

Schema generation must expose the new fields through `schema.json` and
`oas.json`.

## Failure Model

Represent failures as typed Go structs and expose those structs to CEL and
templates. This follows the existing pattern for `contextual.StepOutput`,
`contextual.RunOutput`, and `contextual.TaskExecutionContext`: the runtime keeps
an explicit contract in Go, while recipes see the JSON-tagged field names.

Suggested model:

```go
type FailureKind string

const (
    FailureKindTimeout      FailureKind = "timeout"
    FailureKindTaskError    FailureKind = "task_error"
    FailureKindSystemError  FailureKind = "system_error"
    FailureKindCancellation FailureKind = "cancellation"
    FailureKindUnknown      FailureKind = "unknown"
)

type FailureNodeType string

const (
    FailureNodeOp           FailureNodeType = "op"
    FailureNodeSequence     FailureNodeType = "sequence"
    FailureNodeStateMachine FailureNodeType = "state_machine"
    FailureNodeState        FailureNodeType = "state"
)

type RuntimeFailure struct {
    Kind      FailureKind                    `json:"kind"`
    Code      string                         `json:"code,omitempty"`
    Message   string                         `json:"message"`
    Retryable bool                           `json:"retryable"`
    Node      FailureNode                    `json:"node"`
    Timing    *FailureTiming                 `json:"timing,omitempty"`
    Task      *FailureTask                   `json:"task,omitempty"`
    Artifacts map[string]recipeartifacts.Ref `json:"artifacts,omitempty"`
    Attrs     map[string]interface{}         `json:"attrs,omitempty"`
    Cause     *RuntimeFailure                `json:"cause,omitempty"`
}

type FailureNode struct {
    ID   string          `json:"id,omitempty"`
    Path string          `json:"path,omitempty"`
    Type FailureNodeType `json:"type"`
    Op   string          `json:"op,omitempty"`
}

type FailureTiming struct {
    Timeout string `json:"timeout,omitempty"`
    Scope   string `json:"scope,omitempty"`
    After   string `json:"after,omitempty"`
}

type FailureTask struct {
    Status     string `json:"status,omitempty"`
    ExitCode   *int   `json:"exit_code,omitempty"`
    StderrTail string `json:"stderr_tail,omitempty"`
    StdoutTail string `json:"stdout_tail,omitempty"`
}
```

Place these structs in `pkg/recipe` initially. `NodeMetadata` and
`CatchClause` already define the recipe contract there, and `pkg/template` can
import `pkg/recipe` without introducing a cycle. Keeping the failure contract
next to the catch syntax also makes schema, validation, and compatibility tests
more direct.

The JSON shape remains:

```yaml
kind: timeout | task_error | system_error | cancellation | unknown
code: string
message: string
retryable: boolean
node:
  id: string
  path: string
  type: op | sequence | state_machine | state
  op: string
timing:
  timeout: string
task:
  status: string
artifacts:
  <name>: <artifact-ref>
cause:
  kind: string
  code: string
  message: string
  node:
    path: string
```

Minimum fields for v1:

- `kind`
- `message`
- `retryable`
- `node.id`
- `node.path`
- `node.type`
- `node.op` when the failed node is an op
- `code` when supplied by SWF or c2j
- `cause` when a container re-emits a child failure

Failure normalization should live in the compiler package initially, for
example `pkg/worker/compiler/failure.go`, but it should return
`*recipe.RuntimeFailure`, not an untyped map.

Every executor boundary should preserve normalized failures even when the node
does not have a local catch. For example, `ExecuteOp` should convert task
errors into `recipeFailureError` before returning them, and `runState` should
reuse that propagated failure as the catch input. This lets a state-level catch
match the original op failure (`failure.node.op`, `failure.kind`, and
`failure.code`) instead of only seeing a formatted wrapper such as
`state "implement" execution failed`.

Mapping rules:

- `errors.As(err, *swf.TimeoutError)` or `errors.Is(err,
  context.DeadlineExceeded)` -> `kind: timeout`.
- `swf.IsSystemError(err)` -> `kind: system_error`.
- `swf.IsAppError(err)` -> `kind: task_error`.
- `errors.Is(err, swf.ErrJobCancelled)` or context cancellation ->
  `kind: cancellation`.
- Anything else -> `kind: unknown`.

SWF payload fields should be copied when available:

- `TimeoutError.Payload.Code`, `Scope`, `After`, `Retryable`
- `SystemError.Payload.Code`, `Component`, `Retryable`, `Stacktrace`
- `AppError.Payload.Attrs` into `RuntimeFailure.Attrs` after validating the
  values are JSON-compatible

Do not expose full stack traces or unbounded stderr/stdout by default. If tail
fields are later added, they must be size-capped.

## Template Scope

Add `Failure *recipe.RuntimeFailure` to `templateData`.

Update CEL setup:

```go
cel.Variable("failure", cel.ObjectType("recipe.RuntimeFailure"))
```

Register the failure types with the CEL environment, either through
`ext.NativeTypes` or `cel.Types`, so invalid field names in `catch.when` fail at
compile/validation time. This is the main reason to prefer structs over maps.

Update CEL evaluation inputs to include:

```go
"failure": rc.TemplateData.Failure,
```

Update Go-template roots:

- `resolveSimpleGoTemplateScalar` should accept root `failure`.
- `goTemplateFuncMap` should expose `failure`.

Add a helper on `ResolutionContext`, for example:

```go
func (rc *ResolutionContext) WithFailure(f *recipe.RuntimeFailure) *ResolutionContext
```

It should shallow-copy the resolution context and template data, set
`TemplateData.Failure`, and preserve the existing CEL env, options, transition,
vars, sequence, states, and context values.

Catch conditions and action templates must render against this failure-scoped
context. The failed node's successful current `outputs` are not available unless
the failure object includes partial details.

## Transition Failure

Extend `template.TransitionData` with an optional `Failure` field:

```go
type TransitionData struct {
    From    string                 `json:"from,omitempty"`
    To      string                 `json:"to,omitempty"`
    Payload map[string]interface{} `json:"payload"`
    Failure *recipe.RuntimeFailure `json:"failure,omitempty"`
}
```

`AsMap` should include `failure` only when present. `Clone` must deep-copy both
`Payload` and `Failure`.

Add a constructor for catch-driven transitions:

```go
func NewFailureTransitionData(from, to string, payload map[string]interface{}, failure *recipe.RuntimeFailure) TransitionData
```

The target state should then see:

- `transition.from`
- `transition.to`
- `transition.payload`
- `transition.failure`

## Catch Evaluation

Add an internal evaluator in `pkg/worker/compiler`, for example
`evaluateCatchClauses`.

Inputs:

- catch clauses
- failure map
- source resolution context
- source state name when inside a state machine
- allowed actions for the current scope

Output:

```go
type catchDecision struct {
    Kind      catchDecisionKind // none, route, continueNode, fail
    ClauseID  string
    To        string
    Payload   map[string]interface{}
    Outputs   map[string]interface{}
    Failure   *recipe.RuntimeFailure
    Error     error
}
```

Rules:

1. Evaluate clauses in order.
2. Use `failureCtx.EvaluateCEL(clause.When.String())`.
3. First true clause wins.
4. Render `payload`, `continue.outputs`, or `fail` using the same
   failure-scoped context.
5. A false or non-matching list returns `Kind: none`.
6. CEL or template errors in catch handling are runtime errors and should
   propagate. They are not catchable by the same catch block.

`fail` should create a new normalized failure map with the original failure as
`cause`, then return an error that preserves that map for parent catches.

Use an internal error wrapper for propagated normalized failures:

```go
type recipeFailureError struct {
    Failure *recipe.RuntimeFailure
    Err     error
}
```

Parent catch evaluation should unwrap this error and use its `Failure` as the
incoming cause instead of rebuilding from the formatted error string.

## State-Machine Runtime

Change `runState` to return a state execution result as well as an error.

Suggested shape:

```go
type stateRunResult struct {
    Route *template.TransitionData
}
```

State execution flow:

1. Create `stateResCtx` as today.
2. Resolve state vars as today.
3. Execute the inner node.
4. On success, return no route and no error.
5. On error, extract the propagated normalized child failure when present. If
   the error was not already normalized, build a failure with node type `state`
   and the state context path.
6. Evaluate the state's `metadata.Catch`.
7. If no catch matches, return the normalized failure error.
8. If `to` matches, return `stateRunResult{Route: transition}`.
9. If `continue.outputs` matches, add those outputs to the state context and
   return success.
10. If `fail` matches, return the rewritten failure error.

Main state-machine loop:

- When `runState` returns `Route`, skip normal success transitions, set
  `currentState` to `Route.To`, set `currentTransition` to that transition, and
  continue the loop.
- When `runState` returns success without a route, evaluate normal
  `transitions` exactly as today.
- When `runState` returns an error, evaluate the state-machine node's own
  `metadata.Catch`.

State-machine container catch:

- `to` routes to a state in the same machine and continues execution.
- `continue.outputs` resolves the state-machine node as complete and records
  those outputs on the parent context.
- `fail` rewrites the failure and propagates to the parent.
- no match propagates to the parent.

Validate `to` targets against `stateMap.States` for both state-level and
container-level catches.

## Retry Semantics

The proposal's ordering is:

1. one node attempt fails;
2. catch evaluates;
3. handled catches suppress retry;
4. unhandled failures are eligible for node retry.

To implement that ordering, c2j must own recipe retry scheduling around node
execution. For ops, do not pass the authored retry policy through to SWF as a
multi-attempt policy when catch is present.

Recommended implementation:

- Add a compiler-owned helper such as `executeWithCatchAndRetry`.
- For each recipe-level attempt, execute the node once.
- Use a single-attempt SWF `RunPolicy` for op tasks inside that attempt.
- If catch handles the failure, return the catch decision.
- If catch does not handle the failure, use the authored `RetryPolicy` to decide
  whether to run another recipe-level attempt.
- Preserve existing backoff fields (`initial_interval`, `backoff_coefficient`,
  `maximum_interval`) by awaiting the computed delay through
  `ctx.AwaitDuration`.
- Propagate a catch-aware execution flag from `runState` into child node
  execution when either the state or enclosing state machine has catch clauses.
  This prevents a child op from exhausting SWF retries before the state catch can
  inspect the first failed attempt.

Important compatibility note: SWF `RetryPolicy.MaximumAttempts` means total
attempts. The proposal text describes scalar `retry: 3` as three retries after
the first attempt, but the current recipe type is `swf.RetryPolicy`. Do not add
scalar retry semantics as part of this catch implementation unless the recipe
parser is also intentionally changed.

If no `catch:` is configured on a node or enclosing state machine, keep the
current behavior and pass retry to SWF for ops. That minimizes behavior churn for
existing recipes.

Composite retry should be implemented in the same compiler-owned helper because
`executeCompositeInEnvelope` currently ignores retry.

## `continue` Semantics

For phase 1:

- State `continue.outputs` writes the substitute outputs to the state output
  slot, so normal transitions can read them as `outputs.*`.
- State-machine container `continue.outputs` writes the substitute outputs to
  the parent context and completes the state-machine node.
- Root recipe `continue.outputs` becomes the recipe result.

For phase 2:

- Sequence child `continue.outputs` should write the substitute outputs under
  `sequence.<id>.outputs` and continue with the next sibling.
- Sequence container `continue.outputs` should complete the sequence and skip
  its normal `outputs:` template.

Substitute outputs should not include artifacts unless a future catch action
adds an explicit artifact mapping.

## CEL Helpers

Add helpers through `pkg/template/funcregistry` defaults so they work in both
CEL and Go templates where appropriate:

- `failure_is(failure, kind)`
- `failure_message_contains(failure, text)`
- `failure_message_matches(failure, regex)`
- `failure_has_code(failure, code)`
- `failure_originates_from(failure, node_path_or_id)`
- `failure_root_cause(failure)`

Phase 1 can implement the first four. The origin and root-cause helpers can be
added once nested failure propagation is covered by tests.

Helpers must tolerate nil, missing, or incorrectly typed fields and return false
or nil rather than panicking.

## Recipe Validation

Catch validation should be explicit and run before recipe execution. It should
not depend on a node actually failing during `ExecutionModeValidate`.

Validation has four layers.

### 1. Decode And Schema Validation

The recipe loader and generated JSON schema should reject malformed catch syntax:

- `catch` must be a list of objects.
- `id` must be a string when present.
- `when` must be a string or boolean when present.
- `to` must be a string when present.
- `payload` must be an object and is only valid with `to`.
- `continue` must be an object with optional object field `outputs`.
- `fail` must be an object with known fields only.
- A clause must specify exactly one action: `to`, `continue`, or `fail`.

These are structural errors. They should be reported like existing recipe
authoring errors, not as catchable runtime failures.

### 2. Semantic Catch Validation

Add a compiler validation pass that walks the loaded recipe with scope
information:

- the current node path;
- node type;
- containing state-machine path, if any;
- available state names for that containing state machine;
- template resolution context for the node.

The pass should validate every catch clause, including catch blocks on paths
that might not execute in a particular validation run.

Rules:

- `when` must compile as CEL and return bool.
- `when` compiles with typed `failure`, so field mistakes such as
  `failure.knd` fail validation.
- `to` is valid only when the catch has an enclosing state machine target set.
- `to` must name an existing state in that state machine.
- `continue.outputs` must render successfully against the node scope plus a
  placeholder `failure`.
- `fail.kind`, when provided, must be one of the `FailureKind` constants.
- `fail.message`, `fail.code`, and `fail.cause` templates must render
  successfully when present.
- Unsupported phase-1 catch scopes should fail with a clear error. Phase 1
  supports direct state definitions and state-machine containers. Sequence-step
  catch and non-state-machine root op/sequence catch should produce
  `unsupported_catch_scope` until phase 2 implements them.

Validation should use structured issue codes where available:

```text
catch_invalid_shape
catch_multiple_actions
catch_when_invalid
catch_when_not_bool
catch_to_without_state_machine
catch_to_unknown_state
catch_continue_outputs_invalid
catch_fail_kind_invalid
unsupported_catch_scope
```

### 3. Placeholder Failure

Validation mode should seed a typed representative failure:

```go
var validationFailure = &recipe.RuntimeFailure{
    Kind:      recipe.FailureKindUnknown,
    Message:   "validation placeholder failure",
    Retryable: false,
    Node: recipe.FailureNode{
        ID:   "validation-node",
        Path: "validation/node",
        Type: recipe.FailureNodeState,
    },
}
```

The validator should call `resCtx.WithFailure(validationFailure)` before
compiling catch `when` expressions or rendering catch action templates. This
keeps validation deterministic and lets typed CEL catch compatibility bugs
without requiring artificial task failures.

### 4. Catch Transition Validation

Catch-driven `to` edges are state-machine edges and must be validated alongside
normal transitions.

Build an incoming-edge table for every state:

- initial and normal `transitions` produce an incoming transition with
  `transition.failure == nil`;
- catch `to` clauses produce an incoming transition with
  `transition.failure == validationFailure`;
- catch payload templates are rendered in the source catch context and then used
  as placeholder `transition.payload` for the target edge.

In `ExecutionModeValidate`, validate each target state once for each distinct
incoming edge class it can receive:

- normal incoming edge;
- catch incoming edge;
- both, if the state can be reached both ways.

This matters because a state that references `transition.failure.kind` is valid
on a catch edge but invalid on a normal edge unless it uses optional access or a
defaulting helper. Conversely, normal transition payload references should not
accidentally pass only because a catch placeholder was globally installed.

For `ValidatePathOnly`, validate catch blocks on the selected path plus catch
edges originating from that path. For `ValidateAll`, validate all catch blocks
and all catch-driven edges in the recipe.

## Observability

Extend diagnostics and story data enough to explain catch decisions.

Minimum additions:

- Include selected catch clause ID in logs.
- For catch-driven routes, reuse transition diagnostics but include a flag or
  source field such as `kind: catch`.
- Include `transition.failure` in recipe-test transition payload diagnostics
  after normal redaction.

Optional later additions:

- Dedicated job-story node kind for catch evaluation.
- Per-clause catch condition evaluation records, parallel to transition
  evaluation records.

## Tests

Add focused tests before broad fixture coverage.

Recipe parsing:

- parses `catch` on op, sequence, state-machine, and state nodes;
- rejects multiple actions in one catch clause;
- accepts string and boolean `when` values;
- schema contains `catch`.

Template scope:

- `failure.kind` works in CEL;
- `failure.knd` fails validation because `failure` is typed;
- `${{ failure.message }}` works in payload templates;
- `transition.failure.kind` is visible in the target state.

Validation:

- malformed catch clauses fail during recipe load/schema validation;
- unknown catch target states fail semantic validation;
- catch payload templates render with a typed placeholder failure;
- states reached by both normal and catch transitions are validated with both
  transition shapes;
- unsupported phase-1 catch scopes return `unsupported_catch_scope`.

State-machine execution:

- state op timeout routes through `catch.to`;
- success transitions are unchanged when the state succeeds;
- first matching catch wins;
- unmatched state catch propagates to state-machine catch;
- state-machine fallback catch routes to review state;
- `continue.outputs` lets normal transitions inspect substitute outputs;
- `fail` rewrites the failure and parent catch sees the rewritten failure with
  cause.

Retry:

- matching catch suppresses retry;
- unhandled failure retries according to `RetryPolicy`;
- `fail` remains retry-eligible;
- existing recipes without catch retain SWF retry behavior.

Recipe test harness:

- add a catch-route assertion through existing transition payload diagnostics,
  or add a dedicated assertion after diagnostics support lands.

## Rollout Plan

1. Add recipe model, schema, and parser tests.
2. Add failure scope to template/CEL and helper functions.
3. Add failure normalization and propagated failure wrapper.
4. Implement state-level catch and catch-driven transition failure data.
5. Implement state-machine container catch.
6. Add compiler-owned retry only for nodes with catch or ancestors requiring
   catch-before-retry semantics.
7. Add fixture tests with one timeout path and one task-error path.
8. Update authoring docs listed in the proposal.

## Open Questions

- Should cancellation be catchable by default, or require an explicit opt-in?
- Should `catch.fail` be represented as an `swf.AppError` or stay as an internal
  compiler wrapper until it reaches the job boundary?
- Should artifact refs produced by failed task attempts be exposed through
  `failure.artifacts` in phase 1?
- Should scalar `retry: 3` become supported syntax, or should recipes continue
  using the current `RetryPolicy` object shape?
- Should catch diagnostics be first-class story nodes, or is transition
  diagnostics reuse enough for the initial release?
