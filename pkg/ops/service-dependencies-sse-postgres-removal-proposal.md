# Proposal: Remove `SSE` and `postgres` from Service Dependency APIs

**Date:** 2026-04-21  
**Status:** Design draft  
**Audience:** `pkg/ops`, `pkg/input`, `pkg/worker`, API consumers

## Problem

`ServiceDependencies2` currently carries three concepts:

1. `WorkflowControl`
2. `SSEManager`
3. `Database` (`*gorm.DB`)

Only `WorkflowControl` still looks foundational.

In the current workspace:

- `SSEManager` is only used by the input management API.
- `Database` is only threaded through dependency builders and the op executor.
- A repo-wide scan did not find any non-test op implementation that reads `OpDependencies.Database()`.

That means the core dependency API is carrying transport/storage concepts that are no longer central to the runtime contract.

## Current Baseline

### Core dependency surfaces

- `pkg/ops/service_deps2.go`
  - exposes `SSEManager()` and `Database()`
  - builder exposes `WithSSEManager(...)` and `WithDatabase(...)`
- `pkg/ops/op_dependencies.go`
  - exposes `Database()`
  - builder exposes `WithDatabase(...)`
- `pkg/worker/ops/op_executor.go`
  - reads `deps.Database()`
  - swaps in `swf.TxFromCtx(ctx)` when present

### Input API surfaces

- `pkg/input/runtime.go`
  - stores an `ops.SSEManager`
  - broadcasts `input_completed` and `input_cancelled`
- `pkg/input/management.go`
  - requires `SSEManager` during `Initialize(...)`
  - exposes `GET /api/projects/{projectId}/user-inputs/stream`
- `pkg/input/api/openapi/recipe-input-api.yaml`
  - documents the SSE endpoint and event schemas
- `pkg/input/openapi/generated.go`
  - contains generated SSE transport types
- `pkg/input/sse.go`
  - contains the in-memory `SimpleSSEManager`

### Important observation about current SSE behavior

The current SSE endpoint is already partly poll-based:

- it sends an initial snapshot from `collectPendingInputs(...)`
- it keeps polling pending inputs on a timer
- the in-memory SSE manager is only used for broadcast-style events like cancel/complete

This means SSE is not the sole source of truth today. The authoritative data source is already the workflow query path behind `ListPendingInputs(...)` and `GetDetails(...)`.

There is also current contract drift: the runtime emits `input_completed`, but the OpenAPI enum does not document that event.

### Postgres in the current repo

There are two separate "postgres" concerns today:

1. `postgres` as a service dependency concept (`Database()`, `WithDatabase(...)`)
2. `postgres` as part of the direct SWF runtime test harness

This proposal targets the first one directly.

The second still appears in test/runtime code such as:

- `pkg/input/activity_test.go`
- `pkg/input/test-fixtures/recipe_fixtures_real_engine_test.go`

Those tests start embedded Postgres for the SWF runtime itself. That is a separate decision from removing `Database()` from `ServiceDependencies2`.

## Recommendation

Remove `SSE` and `postgres` from the shared dependency APIs, and remove the user-input SSE HTTP API entirely.

After the change:

- `ServiceDependencies2` should expose only `WorkflowControl()`
- `OpDependencies` should no longer expose `Database()`
- the input management API should be poll-only
- Postgres-backed direct-runtime tests should remain unless we make a separate runtime/testing decision

## Proposed Changes

### 1. Simplify `ServiceDependencies2`

Update `pkg/ops/service_deps2.go` so the interface only contains:

```go
type ServiceDependencies2 interface {
    WorkflowControl() workflowctl.WorkflowControl
    serviceDependenciesMarker()
}
```

Remove:

- `SSEManager()`
- `Database()`
- builder fields for `sseManager` and `database`
- `WithSSEManager(...)`
- `WithDatabase(...)`

Update callers that currently forward those values only as plumbing:

- `pkg/worker/executor/standalone.go`
- `pkg/worker/test-fixtures/recipe_test_framework.go`
- input tests that currently create `NewSimpleSSEManager()`

### 2. Remove DB access from op execution contracts

Update `pkg/ops/op_dependencies.go` to remove:

- `Database() *gorm.DB`
- the stored `db` field
- `WithDatabase(...)`

Update `pkg/worker/ops/op_executor.go` to stop:

- reading `deps.Database()`
- overriding it with `swf.TxFromCtx(ctx)`
- forwarding DB state into `OpDependencies`

Update tests accordingly, especially the dependency-injection test in:

- `pkg/worker/ops/activity_registry_test.go`

The replacement test should assert only the still-supported dependencies, such as `WorkflowControl()`.

### 3. Remove SSE from input runtime and management API

Update `pkg/input/runtime.go`:

- `Runtime` should only hold `WorkflowControl`
- `NewRuntime(...)` should only take `WorkflowControl`
- `NewRuntimeFromDeps(...)` should only read `deps.WorkflowControl()`
- remove event broadcasts from `SubmitResponse(...)` and `Cancel(...)`

Update `pkg/input/management.go`:

- `Initialize(...)` should no longer require an SSE manager
- remove the `/api/projects/{projectId}/user-inputs/stream` route
- delete `SSEStream(...)`

Delete now-unused SSE helper types:

- `pkg/input/sse.go`
- `pkg/input/sse_types_compat.go`

### 4. Remove SSE from the public input API

Update `pkg/input/api/openapi/recipe-input-api.yaml` to remove:

- `/api/projects/{projectId}/user-inputs/stream`
- `InputSSEEventType`
- `InputSSEConnectedData`
- `InputSSEPendingData`
- `InputSSECancelledData`
- `InputSSEHeartbeatData`
- `InputSSEErrorData`
- `InputSSEEvent`

Then regenerate:

- `pkg/input/openapi/generated.go`

The existing pollable REST endpoints remain:

- `GET /api/projects/{projectId}/user-inputs/pending`
- `GET /api/projects/{projectId}/user-inputs/{jobId}`
- `POST /api/projects/{projectId}/user-inputs/{jobId}/respond`
- `POST /api/projects/{projectId}/user-inputs/{jobId}/cancel`

### 5. Module cleanup

After the API cleanup:

- `gorm.io/gorm` should likely be removable from this repo
- `gorm.io/driver/postgres` may also disappear after `go mod tidy`

By contrast, these may remain if direct-runtime tests are kept:

- `github.com/lib/pq`
- embedded Postgres support
- SWF runtime packages that depend on Postgres

## Migration Plan

### Phase 1: Core API cleanup

1. Remove `SSEManager` and `Database` from `ServiceDependencies2`.
2. Remove `Database` from `OpDependencies`.
3. Update worker/executor/test plumbing.

### Phase 2: Input API cleanup

1. Remove SSE behavior from `pkg/input/runtime.go`.
2. Remove the SSE route from `pkg/input/management.go`.
3. Update HTTP tests.
4. Regenerate OpenAPI output.

### Phase 3: Dependency cleanup

1. Run `go mod tidy`.
2. Confirm whether `gorm` disappears completely.
3. Keep Postgres-backed test/runtime dependencies unless explicitly replacing that harness.

## Challenges

### 1. This is an API break for user-input consumers

Removing `/api/projects/{projectId}/user-inputs/stream` is externally visible.

Clients that currently subscribe to SSE will need to switch to polling:

- `GET /pending` for list refresh
- `GET /{jobId}` for details

This is the biggest functional change in the proposal.

### 2. Polling changes latency and load characteristics

SSE gives push-style updates for cancellations/completions. A poll-only design is simpler, but:

- updates become interval-based instead of immediate
- clients may poll more often than today
- UX may need debounce/backoff guidance

If that tradeoff is not acceptable, we should replace SSE with a different push mechanism before removal, not after.

### 3. Out-of-tree users may still depend on `Database()`

Inside this repo, `Database()` appears to be dead plumbing.

However, if external ops or callers rely on:

- `ServiceDependencies2.WithDatabase(...)`
- `OpDependencies.Database()`
- implicit transaction access via `swf.TxFromCtx(ctx)`

they will break.

Before implementation, we should confirm whether this API is used outside the workspace.

### 4. "Remove postgres" is ambiguous

Removing `Database()` from service dependencies does **not** remove Postgres from:

- the direct SWF runtime
- embedded Postgres integration tests
- Postgres-specific runtime dependencies pulled by that harness

If the real goal is "no Postgres anywhere in this repo," that needs a separate proposal covering runtime/test replacement.

### 5. OpenAPI and generated code must move together

The SSE endpoint is represented in:

- handwritten handlers
- the source OpenAPI YAML
- generated transport models
- HTTP tests

Partial cleanup will leave the repo inconsistent.

## Out of Scope

This proposal does not attempt to:

- replace the direct SWF runtime
- remove embedded Postgres from real-engine tests
- introduce a new push transport
- redesign the user-input API beyond removing SSE

## Success Criteria

The change is complete when:

1. `ServiceDependencies2` only exposes `WorkflowControl()`.
2. `OpDependencies` no longer exposes database access.
3. The input API has no SSE route or SSE schemas.
4. Input management still works through the existing REST endpoints.
5. The repo no longer imports `gorm` unless another unrelated feature still requires it.
6. Remaining Postgres usage is limited to the SWF runtime/test harness, or is separately addressed.
