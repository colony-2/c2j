# Proposal: Remove `SSE`, `postgres`, and obsolete ticket/cell-scoped fields by collapsing ticket into job

**Date:** 2026-04-23  
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

Separately, `pkg/contextual` and downstream git/template code still carry ticket and cell-scoped fields that are part of an older execution model where "ticket" and "job" are separate entities:

- `contextual.ActorContext.TicketID`
- `contextual.TicketContext`
- `contextual.TicketCreatorContext`
- `contextual.TicketCreatorUserContext`
- `contextual.TicketCreatorAgentContext`

- `contextual.WorkflowContext.CellID`
- `contextual.WorkflowContext.CellName`
- `contextual.WorkflowContext.CellPath`

At minimum, `CellPath` should be removed. It is no longer a valid product/runtime concept, but it still leaks through template defaults, child recipe launch inputs, git execution context, and workspace scoping.

The target model for this proposal is:

- a job is the work item
- a ticket is not a separate entity
- job/ticket lifecycle is represented by the single job object

This does **not** mean we should discard creator identity. Creator identity still makes sense as job metadata and job context. The distinction is:

- for now, remove actor fields entirely from contextual/runtime state
- if creator identity is needed later, reintroduce it as an explicit job-native concept rather than through the current `actor` shape

Under that model, ticket-specific fields and APIs should be removed rather than preserved as dead metadata.

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

### Contextual and cell-scope surfaces

Ticket and cell-scoped context currently flows through several layers:

- `pkg/contextual/context.go`
  - `ActorContext` exposes `TicketID`, `ActorName`, and `ActorEmail`
  - `JobContext` / `TaskExecutionContext` embed `TicketContext`
  - `TicketCreator*` structs model ticket provenance
- `pkg/template/template_interpolate.go`
  - exposes `context.actor.ticket_id`
  - exposes `context.ticket.*`
- `pkg/template/template_resolver_test.go`
  - has direct coverage for `context.ticket.*` and `context.actor.ticket_id`
- `pkg/starter/start_recipe.go`
  - writes `ticket_id` into recipe job metadata
- `pkg/workflowctl/types.go`
  - `StartJob` already carries `SubmittedAt`
- `pkg/git/gitstate/git_task_context.go`
  - serializes `TicketID`
- `pkg/git/gitstate/metadata.go`
  - includes ticket ID in git metadata emission
- `recipes/new-ticket.yaml`
  - still assumes a ticket-triggered workflow and branches on `context.ticket.*` / `context.actor.ticket_id`
- `recipes/guides/ops/TICKET_MANAGE_OP.md`
  - documents `ticket.manage` as an active op

Cell-scoped fields flow through:

- `pkg/contextual/context.go`
  - `WorkflowContext` exposes `CellID`, `CellName`, and `CellPath`
- `pkg/template/template_interpolate.go`
  - exposes `context.workflow.cell`, `context.workflow.cell_path`, and `context.workflow.cell_id`
- `pkg/ops/git_execution_context.go`
  - duplicates `CellName` and `CellPath`
- `pkg/git/gitstate/git_task_context.go`
  - serializes `CellName` and `CellPath`
- `pkg/git/gitstate/workspace_controller.go`
  - currently requires `cell_path` for scoped persist operations
- `pkg/ops/recipe/op.go`
  - `SingleRecipe` defaults `cell_name` and `cell_path` from contextual workflow fields
- `pkg/ops/recipe/launcher.go`
  - propagates `CellName` and `CellPath` into child `JobContext`

For `root_cell`, there is already a viable source path in config/runtime inspection, but it is distinct from ordinary cell identity:

- `c2j self`
  - `cmd/c2j/internal/configinspect/service.go`
  - resolves `short_name`, `repo`, `ref`, `root_repo`, and `root_ref`
- `pkg/config/config.go`
  - exposes `RootRepo(...)`
  - exposes `RootCell(...)`, though today it is just an alias for `RootRepo(...)`

That means `root_cell` should be treated as its own project-level concept and resolved explicitly from root config, not treated as a rename of `cell` and not populated from the current cell short name by default.

This is not passive metadata. The repo still models ticket and job as distinct today, and `cell_path` is part of the current git persist contract, so removing either requires an explicit migration.

## Recommendation

Remove `SSE` and `postgres` from the shared dependency APIs, remove the user-input SSE HTTP API entirely, collapse ticket into job as a single entity, remove obsolete cell-scoped fields, and add `root_cell` only as separate project-level metadata if still needed.

After the change:

- `ServiceDependencies2` should expose only `WorkflowControl()`
- `OpDependencies` should no longer expose `Database()`
- the input management API should be poll-only
- `contextual` should no longer expose ticket-specific fields
- `contextual` should no longer expose actor-specific fields in the current model
- `contextual.WorkflowContext` should no longer carry `CellID`, `CellName`, or `CellPath`
- `contextual.WorkflowContext` may expose `RootCell`, but only as project-level metadata distinct from ordinary cell identity
- `WorkflowContext.ProjectId` or a clearly equivalent tenant/project field should remain
- `ticket.manage` should no longer exist
- `new-ticket` should become the default job recipe for a cell rather than a special ticket workflow
- the runtime should stop deriving git/template behavior from `context.workflow.cell_path`
- Postgres-backed direct-runtime tests should remain unless we make a separate runtime/testing decision

Recommended broader cleanup in the same change:

- remove:
  - `ActorContext.ActorName`
  - `ActorContext.ActorEmail`
  - `ActorContext.TicketID`
  - `TicketContext`
  - `TicketCreatorContext`
  - `TicketCreatorUserContext`
  - `TicketCreatorAgentContext`
- replace:
  - `WorkflowContext.CellID`
  - `WorkflowContext.CellName`
  - `WorkflowContext.CellPath`
  - optionally add one field such as `WorkflowContext.RootCell` (`json:"root_cell,omitempty"`)
- keep:
  - `WorkflowContext.ProjectId` (or a clearly equivalent tenant/project field)
- remove downstream duplicated ticket and cell fields in git/runtime execution structs
- remove separate ticket-management flows and docs

If `root_cell` remains in the model, it should be resolved explicitly from root-project config. It should not be treated as a rename of `cell`, and it should not default to the current cell short name.

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

### 4. Collapse ticket into job and remove obsolete cell-scoped fields from contextual/runtime plumbing

Preserve these contextual concepts:

- `WorkflowContext.JobID`
- `WorkflowContext.ProjectId` (or equivalent tenant/project identifier)

Update `pkg/contextual/context.go`:

- remove `ActorContext.ActorName`
- remove `ActorContext.ActorEmail`
- remove `ActorContext.TicketID`
- remove `TicketContext`
- remove `TicketCreatorContext`
- remove `TicketCreatorUserContext`
- remove `TicketCreatorAgentContext`
- remove `WorkflowContext.CellID`
- remove `WorkflowContext.CellName`
- remove `WorkflowContext.CellPath`
- optionally add separate project-level metadata such as:

```go
type WorkflowContext struct {
    RootCell  string `json:"root_cell,omitempty"`
    JobID     string `json:"job_id,omitempty"`
    ProjectId string `json:"project_id,omitempty"`
}
```

Update template/runtime projections so they no longer expose removed fields:

- `pkg/template/template_interpolate.go`
  - stop emitting `context.actor.ticket_id`
  - stop emitting `context.actor.actor_name`
  - stop emitting `context.actor.actor_email`
  - stop emitting `context.ticket.*`
  - stop emitting `context.workflow.cell_path`
  - stop emitting `context.workflow.cell`
  - stop emitting `context.workflow.cell_id`
  - emit `context.workflow.root_cell` only if that field remains in the model
  - keep `context.workflow.job_id`
  - keep `context.workflow.project_id`
- CEL/context tests that currently assert those fields

Remove duplicated ticket/cell fields from execution structs:

- `pkg/ops/git_execution_context.go`
  - remove `CellPath`
  - recommended: remove `CellName`
- `pkg/git/gitstate/git_task_context.go`
  - remove `TicketID`
  - remove `CellPath`
  - recommended: remove `CellName`
  - remove `GetTicketID()`
  - remove `GetCellPath()`
  - recommended: remove `GetCellName()`

Update recipe child-job APIs that currently default from contextual fields:

- `pkg/ops/recipe/op.go`
  - remove `SingleRecipe.CellPath`
  - remove `SingleRecipe.CellName`
  - remove defaults using `{{ context.workflow.cell_path }}`
  - remove defaults using `{{ context.workflow.cell }}`
  - add `root_cell` only if child recipes truly need project-level overseer identity
- `pkg/ops/recipe/launcher.go`
  - stop copying removed workflow fields into child `JobContext`
  - copy `RootCell` only if child jobs need project-level root-cell metadata

Update CLI/runtime entrypoints that currently synthesize ticket/cell fields:

- `cmd/c2j/internal/submitjob/service.go`
  - stop writing `TicketID`
  - stop writing `CellPath: "."`
  - stop writing `CellName`
  - do not populate `RootCell` from the current cell short name
  - if `root_cell` is retained, resolve it from root config (`root_repo` -> explicit root-cell resolution) or extend `c2j self` / config inspection to provide it directly

Update recipe/job metadata so ticket/cell metadata stops leaking into job headers:

- `pkg/starter/start_recipe.go`
  - remove `MetaFieldActorEmail`
  - remove `MetaFieldTicketID`
  - remove `MetaFieldCellID`
  - remove `MetaFieldCellName`
  - remove corresponding fields from `JobMetadata`
  - optionally add `MetaFieldRootCell` only if project-level root-cell metadata is still needed

Update user-facing context docs and ticket/job recipes:

- `recipes/guides/TASK_EXECUTION_CONTEXT_REFERENCE.md`
  - remove `context.actor.ticket_id`
  - remove `context.ticket.*`
  - remove `context.workflow.cell*`
- `recipes/new-ticket.yaml`
  - reframe it as the default job recipe for a cell
  - remove assumptions that a separate ticket entity exists
  - replace `context.ticket.*` / `context.actor.ticket_id` usage with job-native inputs/context
- `recipes/guides/ops/TICKET_MANAGE_OP.md`
  - remove it; `ticket.manage` no longer exists in the unified job model

Update any runtime/API surface that still distinguishes ticket from job:

- job metadata, docs, and helper names should stop describing a parallel ticket entity
- any "create ticket" flow should become "start job" or equivalent job-native behavior

Replace the current git scoping behavior that hard-requires `cell_path`:

- `pkg/git/gitstate/workspace_controller.go`
  - stop treating `cell_path` as required scope input
  - default persist/restore scope to repo root (`"."`) unless a new explicit scoping primitive is introduced

That repo-root fallback is the simplest replacement consistent with "cell path is no longer a concept."

### 5. Remove SSE from the public input API

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

### 6. Module cleanup

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

### Phase 3: Contextual and git/template cleanup

1. Remove ticket structs and fields from `pkg/contextual`, template exposure, and job metadata.
2. Remove `CellID` / `CellName` / `CellPath` from `pkg/contextual`.
3. Remove `context.workflow.cell_path` and `context.workflow.cell` from template exposure and defaults.
4. Add `root_cell` only if project-level overseer metadata is still required, and resolve it explicitly from root config rather than current-cell identity.
5. Remove `ticket.manage` and related docs/surfaces.
6. Reframe `new-ticket` as the default cell job flow rather than a separate ticket workflow.
7. Update child recipe inputs, launch propagation, starter metadata, and CLI submit paths.
8. Replace `cell_path`-based git scoping with repo-root behavior or another explicit scope input.

### Phase 4: Dependency cleanup

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

### 4. Removing `cell_path` changes git persist semantics

Today `pkg/git/gitstate/workspace_controller.go` requires `cell_path` and rejects repository-root scope.

If `cell_path` is removed, we need to choose one of these behaviors:

- persist the full repository by default
- introduce a new explicit scoping input unrelated to contextual workflow fields

The proposal recommends full-repository scope because it matches the claim that cell path is no longer a concept, but that is still a behavioral change with real git-side consequences.

### 5. Template and recipe defaults will break

The current repo still references:

- `{{ context.actor.ticket_id }}`
- `{{ context.ticket.* }}`
- `{{ context.workflow.cell_path }}`
- `{{ context.workflow.cell }}`

in defaults, tests, and fixtures.

Removing those fields means:

- ticket-aware recipes and tests must change
- recipe defaults in `pkg/ops/recipe/op.go` must change
- template tests and fixture recipes must change
- any out-of-tree recipes using those contextual fields will break

If `root_cell` is retained, it should be introduced as new project metadata, not as a syntactic replacement for `context.workflow.cell`.

The creator-identity path should remain intact:

- no creator-specific actor fields are retained in this proposal for now
- if creator identity returns later, it should be added back as a separate job-native shape

### 6. Collapsing ticket into job is larger than dependency cleanup

The separate ticket model is still wired into the repo today through:

- `recipes/new-ticket.yaml`
- `ticket.manage`
- starter job metadata (`pkg/starter/start_recipe.go`)
- template/context docs and tests

So the target model is coherent, but it is broader than merely removing ticket service dependencies. It requires functional migration to a job-native model.

### 7. `new-ticket` needs semantic migration, not just deletion

If `new-ticket` becomes the default job for a cell, then the migration should say that explicitly:

- `new-ticket` is no longer a ticket workflow layered on top of jobs
- it is the default cell job recipe
- any former "ticket creation" side effects become ordinary job-start behavior

That means the proposal should treat `new-ticket` as a retained workflow with new semantics, not as legacy ticket-only surface area.

### 8. `root_cell` needs explicit semantics and source of truth

`root_cell` is distinct from `cell`.

That means:

- it should not be described as a replacement for current cell identity
- it should not default from the current cell short name
- `c2j self` / config inspection likely needs an explicit `root_cell` field if we want runtime code to consume it cleanly

Today `RootRepo` and `RootRef` exist, but root-cell naming is not exposed cleanly as first-class metadata.

### 9. "Remove postgres" is ambiguous

Removing `Database()` from service dependencies does **not** remove Postgres from:

- the direct SWF runtime
- embedded Postgres integration tests
- Postgres-specific runtime dependencies pulled by that harness

If the real goal is "no Postgres anywhere in this repo," that needs a separate proposal covering runtime/test replacement.

### 10. OpenAPI and generated code must move together

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
- design a new fine-grained git scoping model after `cell_path` removal

## Success Criteria

The change is complete when:

1. `ServiceDependencies2` only exposes `WorkflowControl()`.
2. `OpDependencies` no longer exposes database access.
3. `contextual.WorkflowContext` no longer carries `CellPath`.
4. `contextual` no longer exposes ticket-specific fields.
5. `contextual.WorkflowContext` no longer carries `CellID`, `CellName`, or `CellPath`.
6. If `root_cell` remains, it is modeled as separate project metadata with explicit semantics and source of truth.
7. No runtime/template code depends on `context.actor.*`, `context.ticket.*`, or `context.workflow.cell_path`.
8. `ticket.manage` no longer exists as an op or documented API.
9. `new-ticket` is treated as the default job recipe for a cell rather than a separate ticket workflow.
10. The input API has no SSE route or SSE schemas.
11. Input management still works through the existing REST endpoints.
12. The repo no longer imports `gorm` unless another unrelated feature still requires it.
13. Remaining Postgres usage is limited to the SWF runtime/test harness, or is separately addressed.
