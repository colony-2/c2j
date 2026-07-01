# Plan: Job Context Env Propagation And Child Job Listing

## Status

Implemented in this workspace. Verification: `go test ./...` passes.

## Problem

Command and extension ops can launch arbitrary processes, including a nested
`c2j` process that submits more recipe jobs. Those jobs are currently submitted
as ordinary top-level jobs unless the recipe used one of the built-in child
recipe ops.

That leaves two gaps:

- a process launched from a running job does not automatically know which c2j
  job and invocation launched it
- a nested `c2j submit` cannot persist a parent-child relationship unless the
  caller passes custom inputs or metadata manually

We want the runtime to provide current-job context through environment
variables, and we want c2j job submission to convert that environment contract
into durable JobDB metadata.

## Goals

- Every `command_execution` op process receives current c2j job context env vars.
- Every selector-backed extension op process receives the same env vars.
- The env vars are available inside `sandbox.type=shai` executions as well as
  host executions.
- `c2j submit` detects those env vars and records the launching job as the
  submitted job's parent metadata.
- A command or extension op automatically adds the jobs submitted from that op
  invocation to its output, so downstream steps can decide whether to wait for
  them.
- Parent-linked nested submissions are limited to the current tenant, matching
  the SWF wait model for jobs.
- Native recipe child launches use the same parent metadata, so `recipe.run`,
  `recipes.run`, and `recipe.child_group.*` children show up in the same list.
- Add a `c2j list children` command that lists direct child jobs by using the
  same parent metadata.
- Scope child listing to children started by the current op invocation by
  default, with an explicit flag to list children from all ops in the current
  parent job.
- Keep the implementation centered on public package surfaces where practical,
  especially `pkg/recipejob`, instead of duplicating CLI internals.

## Non-Goals

- No migration or backfill for jobs submitted before this metadata exists.
- No full DAG query in the first pass. The new command lists direct children
  only.
- No authorization or anti-spoofing guarantee. Environment-derived metadata is
  lineage information, not a security boundary.
- No change to JobDB's core schema. The c2j JobDB chapter schema must be
  extended so recipe start payloads accept `parent` and activity output
  envelopes accept `jobs`.

## Current Code Points

- Job submission payload assembly lives in `pkg/recipejob/start.go`.
- JobDB metadata for recipe jobs is built in `pkg/starter/start_recipe.go` via
  `JobMetadataFromStartJob`.
- c2j CLI submission enters at `cmd/c2j/internal/submitjob/service.go`.
- c2j list behavior is implemented in `cmd/c2j/internal/listjobs`.
- Public recipe job listing helpers already exist in `pkg/recipejob/list.go`.
- Command ops build process env in
  `pkg/worker/commandop/command_execution.go`.
- Extension ops build process env in `pkg/ops/extensions/execution_op.go`.
- Host and shai sandbox process execution both flow through
  `pkg/ops/process/runtime.go`.
- The op executor already has the current JobDB key through `ops.JobTool`, and
  invocation identity through `ActivityInvocationRequest.GitTaskContext`.

## Env Contract

Add a small shared package for the env contract, for example
`pkg/jobcontext`.

The command and extension op runtimes should inject these protected variables:

- `C2J_CURRENT_CONTEXT_VERSION=1`
- `C2J_CURRENT_TENANT_ID`
- `C2J_CURRENT_JOB_ID`
- `C2J_CURRENT_JOB_TYPE`
- `C2J_CURRENT_OP_TYPE`
- `C2J_CURRENT_OP_STEP`
- `C2J_CURRENT_OP_TASK_TYPE`
- `C2J_CURRENT_CELL_NAME`
- `C2J_CURRENT_REPOSITORY_SOURCE`
- `C2J_CURRENT_GIT_REF`
- `C2J_CURRENT_INVOCATION_PATH`
- `C2J_CURRENT_INVOCATION_SEQUENCE`
- `C2J_CURRENT_INVOCATION_HASH`

Also inject `C2J_TENANT_ID` with the current tenant ID for tenant-only child
tools. The current c2j CLI resolves its tenant from `--jobdb`, `C2J_JOBDB`, or
project config; when current-job env is present, the resolved JobDB tenant must
match `C2J_CURRENT_TENANT_ID`.

Rules:

- The `C2J_CURRENT_*` values describe the currently running job and op
  invocation.
- When a nested `c2j submit` sees these vars, it records them as the submitted
  job's `parent_*` metadata.
- `C2J_CURRENT_TENANT_ID` and `C2J_CURRENT_JOB_ID` are the minimum required pair
  for parent metadata. Missing or blank values mean "no parent context".
- Invalid numeric values, such as a non-integer invocation sequence, should fail
  submission with a clear error instead of silently dropping partial context.
- Runtime-injected `C2J_CURRENT_*` vars should override command input env or
  extension manifest env. A nested parent-linked submit must stay in the current
  tenant, so a `--jobdb` value resolving to a different tenant should fail fast.

Suggested shared types:

```go
type Current struct {
    TenantID           string
    JobID              string
    JobType            string
    OpType             string
    OpStep             string
    OpTaskType         string
    CellName           string
    RepositorySource   string
    GitRef             string
    InvocationPath     string
    InvocationSequence int64
    InvocationHash     string
}

type Parent struct {
    TenantID           string
    JobID              string
    JobType            string
    OpType             string
    OpStep             string
    OpTaskType         string
    CellName           string
    RepositorySource   string
    GitRef             string
    InvocationPath     string
    InvocationSequence int64
    InvocationHash     string
}
```

The package should expose helpers like:

- `EnvForCurrent(Current) map[string]string`
- `ParentFromEnv(getenv func(string) string) (Parent, bool, error)`
- `CurrentFromEnv(getenv func(string) string) (Current, bool, error)`
- `MergeProtectedEnv(base map[string]string, protected map[string]string)`

## Metadata Contract

Extend recipe job metadata with top-level parent fields so JobDB metadata
filters can target them directly:

- `parent_tenant_id`
- `parent_job_id`
- `parent_job_type`
- `parent_op_type`
- `parent_op_step`
- `parent_op_task_type`
- `parent_cell_name`
- `parent_repo`
- `parent_git_ref`
- `parent_invocation_path`
- `parent_invocation_seq`
- `parent_invocation_hash`

Add matching field constants near the existing metadata constants in
`pkg/starter/start_recipe.go`, for example:

- `MetaFieldParentTenantID`
- `MetaFieldParentJobID`
- `MetaFieldParentInvocationPath`

Keep these fields optional. Existing jobs will decode with empty parent data.
The first implementation can keep `JobMetadataVersion` at `1` because this is a
backward-compatible metadata extension. Bump the version only if JobDB indexing
or downstream consumers require a versioned field set.

Update list DTOs:

- add `Parent *jobcontext.Parent` or flattened parent fields to
  `recipejob.RecipeJob`
- populate the fields in `RecipeJobFromSummary` from metadata first
- optionally fall back to the start payload if `workflowctl.StartJob` also
  carries parent context

## Start Payload Plumbing

Add parent context to `workflowctl.StartJob`:

```go
Parent *jobcontext.Parent `json:"parent,omitempty"`
```

Then thread it through:

- `pkg/recipejob.BuildStartJobRequest`
  - add `Parent *jobcontext.Parent`
  - copy the value into `workflowctl.StartJob.Parent`
- `pkg/starter.JobMetadataFromStartJob`
  - copy parent fields from `startJob.Parent` into `starter.JobMetadata`
- `pkg/recipejob.JobMetadataFromRaw`
  - decode the new optional fields

This makes parent context durable in both the payload and the list-friendly
metadata.

## Process Env Injection

Build current-job env once in `pkg/worker/ops/op_executor.go`, because that is
the common point with access to:

- current tenant/job via `jobTool.GetJobKey()`
- op type and step via `ActivityRegistration`
- cell/repo/git/invocation via `req.GitTaskContext`

Add the current context to `OpDependencies` rather than forcing each op to
reconstruct it:

- extend `ops.OpDependencies` with `CurrentJobContext() jobcontext.Current`
- add builder storage and `WithCurrentJobContext(...)`
- populate it in `op_executor.do`

Then use the helper from the process-launching ops:

- in `pkg/worker/commandop/command_execution.go`, merge configured env first,
  then overlay `jobcontext.EnvForCurrent(deps.CurrentJobContext())`
- in `pkg/ops/extensions/execution_op.go`, merge manifest env first, then
  overlay the same protected current-job env

Because both ops already pass `RunRequest.Env` into `process.ExecuteProcess`,
the injected env will be present for both host execution and shai execution.
`executeInShai` already passes `BuildProcessEnvMap(req.Env)` to
`shai.SandboxExec.Env`.

## c2j Submit Behavior

Update `cmd/c2j/internal/submitjob/service.go`:

1. After options are completed and validated, call
   `jobcontext.ParentFromEnv(os.Getenv)`.
2. If no current-job env exists, continue with existing top-level submit
   behavior.
3. If current-job env exists, require `opts.TenantID == parent.TenantID`.
   `opts.TenantID` is the tenant resolved from `--jobdb`, `C2J_JOBDB`, or
   project config. Return a clear error if a caller tries to submit a
   parent-linked job to a different tenant.
4. If current-job env exists, pass the parsed parent context into
   `recipejob.BuildStartJob`.
5. Include parent fields in JSON output only if useful; the durable contract is
   metadata, not CLI stdout.

Do not require a new submit flag in the first pass. The feature should be
automatic when c2j is launched from a c2j-managed command or extension op.

Optional escape hatch:

- add `--no-parent-context` only if tests or real workflows show a need to
  submit an unrelated top-level job from inside an op; parent-linked submissions
  must remain same-tenant

## Started Job Context

At the end of every command and extension op invocation, c2j should list the
direct children whose parent metadata matches the current job and invocation:

- `parent_tenant_id == C2J_CURRENT_TENANT_ID`
- `parent_job_id == C2J_CURRENT_JOB_ID`
- `parent_invocation_hash == C2J_CURRENT_INVOCATION_HASH`

This is the same filter used by default for `c2j list children`. It captures
jobs started by nested `c2j submit` processes without requiring the submitter to
write a side-channel file, and it naturally includes all jobs started during the
same op invocation.

Add compact public context types, for example in `pkg/recipejob` or
`pkg/jobcontext`:

```go
type StartedJobContext struct {
    TenantID              string `json:"tenant_id"`
    JobID                 string `json:"job_id"`
    RecipeName            string `json:"recipe,omitempty"`
    Status                string `json:"status,omitempty"`
    ParentInvocationHash  string `json:"parent_invocation_hash,omitempty"`
}

type StartedJobsContext struct {
    JobIDs []string            `json:"job_ids,omitempty"`
    Items  []StartedJobContext `json:"items,omitempty"`
}
```

Template/context shape:

- add `Jobs StartedJobsContext json:"jobs,omitempty"` to
  `contextual.StepOutput`
- add `Jobs StartedJobsContext json:"jobs,omitempty"` to
  `contextual.RunOutput`
- expose it as a sibling of `outputs` and `artifacts`:
  - `sequence.my_node.jobs.job_ids`
  - `sequence.my_node.jobs.items`
  - `sequence.my_node.runs[0].jobs.job_ids`
  - `states.my_state.jobs.job_ids`

Rules:

- Collect started jobs after the child process exits, on both success and
  failure paths.
- Do not add started-job data to `CommandExecutionOutput` or extension op
  output maps. Runtime job context is not part of the op's value output; it is
  execution metadata like artifacts.
- Extension output schemas do not need to mention jobs.
- If collecting started jobs fails, fail the op after the process exits. If the
  process also failed, join the errors so the original process failure is not
  hidden.
- Fetch all pages for this invocation-scoped query. The output must represent
  all jobs known to have been started by the op, not only the first page.
- Because nested submission is same-tenant only, every `jobs.job_ids` value is
  safe for downstream same-tenant wait operations such as `AwaitJobs`.

Implementation points:

- Add a helper in `pkg/worker/ops/op_executor.go` that collects started jobs
  from `workflowctl.WorkflowControl` using the same parent metadata filter.
  Keep it local to the worker package to avoid an import cycle with
  `pkg/recipejob`.
- Call that helper once after `reg.Step.Invoke` returns. This covers command
  ops, extension ops, native recipe child ops, success paths, and failure
  payloads through the common activity executor.
- Thread the collected jobs through `workerops.ActivityInvocationOutput` as a
  sibling to `OpOutput` and `ArtifactRefs`, for example
  `Jobs StartedJobsContext json:"jobs,omitempty"`.
- In `pkg/worker/compiler/compiler.go`, read `decoded.Activity.Jobs` and store
  it with the step alongside `stepInput` and `stepArtifacts`.
- Extend `template.ResolutionContext.AddExecutionWithArtifactData` or add a new
  sibling method so callers can record outputs, artifacts, and jobs together.
- Keep the collection helper no-op if there is no current job context, so unit
  tests and direct op command execution without a job still work.
- Update `pkg/jobdbschema` so activity output payloads accept the new `jobs`
  sibling.

## Validation And CEL Output Shape

Validation mode must know about the automatic `jobs` sibling. Otherwise a recipe
that references `sequence.some_op.jobs.job_ids` can fail validation even though
runtime will produce that field.

Relevant current paths:

- `pkg/worker/compiler/validation_context.go`
  - `validationJobContext.DoTask` does not execute the real op
  - it synthesizes `zeroOutput` from the op output type
  - for `extension_execution`, it special-cases selector manifests and uses
    `resolved.ZeroOutput()`
- `pkg/worker/compiler/validation_helpers.go`
  - `zeroOutputForOp` builds future-output placeholders used by validation
  - selector ops also use manifest-derived zero output here
- `pkg/template/template_resolver.go`
  - `AddExecutionWithArtifactData` stores the synthesized output map that CEL
    expressions read as `sequence.<id>.outputs` and `states.<id>.outputs`
  - it should also be able to store started jobs so CEL can read
    `sequence.<id>.jobs`

Implementation requirements:

- Add a shared helper next to the started-job context DTOs. For validation
  placeholders it should behave like:

```go
func EmptyStartedJobsContext() StartedJobsContext {
    return StartedJobsContext{
        JobIDs: []string{},
        Items:  []StartedJobContext{},
    }
}
```

- Use the helper anywhere validation constructs `template.StepOutput`
  placeholders for command or extension ops.
- `validation_context.go` should include `Jobs: EmptyStartedJobsContext()` in
  the synthesized `ActivityInvocationOutput` for command and extension ops.
- `validation_helpers.go` should include the same empty jobs context when it
  builds future-output placeholders.
- `template/cel_vars.go` must include `jobs` when it clamps `StepOutput` and
  `RunOutput`, next to `outputs`, `artifacts`, and `runs`.
- Extension manifest output schema validation remains focused on extension-owned
  value output. Jobs are not part of the extension output schema, so
  `additionalProperties: false` has no effect on `sequence.my_node.jobs`.

## Native Recipe Child Launches

Set the same parent metadata when jobs are started by built-in recipe child ops:

- `pkg/ops/recipe/launcher.go`
  - accept a `jobcontext.Parent` or build one from `parentJobKey` and the current
    invocation
  - set `workflowctl.StartJob.Parent`
- `pkg/ops/recipe/op.go`
  - pass parent context for `recipe.run_and_get_result`, `recipes.run`, and
    `recipes.run_and_wait`
- `pkg/ops/recipe/child_group.go`
  - set the same parent context for each child group child

This ensures `c2j list children` shows both explicit nested c2j submissions and
first-class recipe children.

## Child Listing API

Add public helpers in `pkg/recipejob`:

```go
type ListChildRecipeJobsRequest struct {
    TenantID                  string
    ParentTenantID            string
    ParentJobID               string
    ParentInvocationHash      string
    AllParentInvocations      bool
    Statuses                  []jobdb.JobStatus
    Stores                    []jobdb.JobStore
    CreatedAfter              *time.Time
    CreatedBefore             *time.Time
    PageSize                  int
    PageToken                 string
}

func BuildListChildJobsRequest(req ListChildRecipeJobsRequest) (jobdb.ListJobsRequest, error)
func ListChildRecipeJobs(ctx context.Context, lister Lister, req ListChildRecipeJobsRequest) (ListRecipeJobsResponse, error)
```

The built JobDB request should:

- require `TenantID`, `ParentTenantID`, and `ParentJobID`
- require `ParentInvocationHash` unless `AllParentInvocations` is true
- filter `JobTypes` to `starter.RecipeJobType`
- add metadata filter:
  - `parent_tenant_id == ParentTenantID`
  - `parent_job_id == ParentJobID`
  - `parent_invocation_hash == ParentInvocationHash` unless
    `AllParentInvocations` is true
- combine those filters with `AndFilter`
- preserve pagination and status filtering semantics from `ListRecipeJobs`

Return `RecipeJob` values instead of generic `jobRow` values so callers get the
same list-friendly recipe metadata plus parent fields.

## New CLI Command

Add a subcommand under `list`:

```text
c2j list children
```

Behavior:

- default the selected tenant using existing `--jobdb` / `C2J_JOBDB` / project
  config rules
- default `--parent-tenant-id` and `--parent-job-id` from
  `C2J_CURRENT_TENANT_ID` and `C2J_CURRENT_JOB_ID`
- default `--parent-invocation-hash` from
  `C2J_CURRENT_INVOCATION_HASH`, so the command returns children started by
  this op invocation
- support `--all-ops` to omit the invocation filter and return all direct
  children of the selected parent job, across every op invocation
- support explicit `--parent-tenant-id` and `--parent-job-id` for local
  debugging outside a running job
- support explicit `--parent-invocation-hash` for local debugging of the default
  invocation-scoped mode outside a running job
- support `--status`, `--created-after`, `--created-before`, `--page-size`,
  `--page-token`, `--all`, `--json`, and `--embed`
- emit `no child jobs found` for empty table output
- include the decoded `parent` object in JSON output

Implementation shape:

- add `cmd/c2j/internal/childjobs/options.go`
- add `cmd/c2j/internal/childjobs/service.go`
- add `cmd/c2j/internal/cmd/list_children.go`
- update `cmd/c2j/internal/cmd/list.go` so `newListCmd()` keeps its existing
  list behavior and also registers `newListChildrenCmd()`

The child command can share small parsing helpers with `listjobs` by moving
status/time/page parsing to an internal common package if direct reuse would
otherwise create awkward duplication.

## Precedence And Edge Cases

- If an op author sets `C2J_CURRENT_JOB_ID` in command env or extension
  manifest env, runtime-injected values win.
- If a nested `c2j submit --jobdb ...` resolves to a different tenant while
  current-job env is present, submission fails because parent-linked children
  must stay in the current tenant.
- `c2j list children` lists children in the selected tenant. For jobs created
  through this feature, the child tenant and parent tenant are the same.
- By default, `c2j list children` requires current invocation context or an
  explicit `--parent-invocation-hash`. Passing `--all-ops` removes that
  requirement.
- If only one of `C2J_CURRENT_TENANT_ID` or `C2J_CURRENT_JOB_ID` is present,
  treat that as malformed context and fail nested submit with a clear message.
- If no current context env is present, `c2j submit` remains unchanged and
  `c2j list children` requires explicit parent flags.
- If a command or extension op starts no jobs, it should return an empty or
  omitted `jobs` value, not an error.

## Test Plan

### Unit Tests

- `pkg/jobcontext`
  - env construction includes the expected variable names
  - env parsing rejects partial parent context
  - env parsing rejects non-integer invocation sequence
  - protected merge overwrites existing `C2J_CURRENT_*` values

- `pkg/starter`
  - `JobMetadataFromStartJob` copies parent fields
  - old metadata without parent fields still decodes

- `pkg/recipejob`
  - `BuildStartJob` preserves parent context
  - `BuildListChildJobsRequest` builds an AND metadata filter for parent tenant
    and parent job
  - default child listing adds a `parent_invocation_hash` metadata filter
  - `AllParentInvocations` omits the `parent_invocation_hash` metadata filter
  - `RecipeJobFromSummary` exposes parent fields from metadata
  - started-job context projection returns both detailed jobs and a flat
    same-tenant job ID list

- `pkg/template`
  - `StepOutput` exposes a `jobs` sibling alongside `outputs` and `artifacts`
  - `RunOutput` exposes a `jobs` sibling alongside `outputs` and `artifacts`
  - CEL clamping includes `jobs` for steps and runs

### Op Runtime Tests

- `pkg/worker/commandop`
  - command output can print `C2J_CURRENT_JOB_ID` and
    `C2J_CURRENT_INVOCATION_HASH`
  - user-provided command env cannot override protected current-job env
  - `C2J_TENANT_ID` defaults to current tenant
  - after a nested same-tenant `c2j submit`, the activity output envelope
    includes `jobs.job_ids` and `jobs.items`
  - if the command process fails after submitting a child job, failed task
    metadata still includes the jobs context where possible

- `pkg/ops/extensions`
  - extension op output can print the current-job env vars
  - manifest env cannot override protected current-job env
  - existing ambient env inheritance behavior remains intact
  - after a nested same-tenant `c2j submit`, the activity output envelope
    includes `jobs.job_ids` and `jobs.items`
  - extension output schema validation remains scoped to extension-owned output
  - if the extension process fails after submitting a child job, failed task
    metadata still includes the jobs context where possible

- `pkg/ops/process`
  - host execution still merges ambient env and request env as before
  - shai execution receives request env through `SandboxExec.Env`

### Compiler Validation Tests

- `pkg/worker/compiler`
  - validation mode allows downstream CEL references to
    `sequence.command.jobs.job_ids`
  - validation mode allows downstream CEL references to
    `sequence.command.jobs.items`
  - validation mode allows downstream CEL references to
    `sequence.extension.jobs.job_ids`
  - validation mode allows downstream CEL references to
    `sequence.extension.jobs.items`
  - future-output placeholders include jobs context for command ops
  - future-output placeholders include jobs context for selector-backed
    extension ops
  - selector-backed extension validation still respects the extension's declared
    output schema for extension-owned fields
  - selector-backed extension validation still exposes `jobs` when
    the extension output schema uses `additionalProperties: false`

### Submit And List Tests

- `cmd/c2j/internal/submitjob`
  - with no current env, submitted metadata has no parent fields
  - with current env, submitted metadata has parent fields
  - with current env, `--jobdb` resolving to the current tenant succeeds
  - with current env, `--jobdb` resolving to a different tenant fails clearly
  - malformed partial current env returns a clear error

- `cmd/c2j/internal/childjobs`
  - defaults parent key from current env
  - defaults parent invocation hash from current env
  - `--all-ops` lists children from every parent op invocation
  - explicit parent flags work outside a running job
  - JSON output includes child jobs and decoded parent context
  - table output handles no children and pagination

### End-To-End Regression

- Create a parent recipe with a `command_execution` step that runs nested
  `c2j submit`.
- Run the parent job.
- In a command/extension op, run a nested `c2j submit` and then
  `c2j list children`; verify the nested job from that same op invocation
  appears by default.
- Verify downstream steps can read the submitted child through
  `sequence.<op_id>.jobs.job_ids`.
- From a different op invocation in the same parent job, run
  `c2j list children --all-ops` and verify nested jobs from other op
  invocations also appear.
- Verify nested `c2j submit --jobdb <different tenant>` fails from inside an op
  with current-job env.
- Verify native `recipe.run` and `recipe.child_group.*` children appear in the
  same `c2j list children --all-ops` output.

## Rollout Notes

- The feature is backward compatible for existing jobs because all new metadata
  fields are optional.
- New child listing only works for jobs submitted after this change.
- The env contract should be documented as best-effort lineage context. It is
  intentionally easy for local tools and extension authors to consume, but it is
  not an authentication mechanism.
