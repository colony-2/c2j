# Cortex Recipe Job API Guide

This guide covers the public c2j surface added for Cortex-style hosts that use
remote JobDB and must not import `cmd/c2j/internal/*`.

The package is:

```go
import "github.com/colony-2/c2j/pkg/recipejob"
```

## Contract

Submission returns captured tenant, job, cell name, immutable cell ID and repository
identity in `jobref.Ref`. Treat that returned identity as authoritative; do not
reconstruct a receipt from a later registry lookup. Both goal-bearing legacy
handles and goal-free handles are accepted. `GoalID` is optional passive history.
Current list/read/child/story/context projections omit live goal identity.

Submitted nonblank string `inputs.title` is stored as optional metadata `title`
(`starter.JobMetadata.Title`, `starter.MetaFieldTitle`). `RecipeJob.Title` and
`joblist.Job.Title` expose it alongside `prompt`. These projections use summary
metadata without additional reads. Stored titles remain verbatim; empty titles,
whitespace-only values and secret references are not indexed. Cortex owns its
display fallback using available job fields. CLI users can set
`c2j submit "prompt" --title "Job title"`.

The public `pkg/recipejob` package exposes:

- target resolution with c2j CLI-compatible cell rules
- start-request assembly for `workflowctl.StartJob`
- recipe-job list/read helpers backed by JobDB `ListJobs`
- reusable c2j list defaults for visible statuses and stores
- operator recovery of jobs blocked by crash concern

It does not open a JobDB runtime, start workers, execute recipes, or register
ops.

For read-only remote listing from explicit connection and repository inputs,
use the supported [`pkg/joblist` API](pkg/joblist/README.md). It opens its own
remote listing connection and returns typed job/execution views without local
configuration discovery or worker initialization. The recipe-specific helpers
in this guide remain available for broader host integrations.

## Resolve A Target

Use `ResolveTarget` anywhere Cortex accepts the same target values as c2j:

```go
target, err := recipejob.ResolveTarget(ctx, recipejob.ResolveTargetRequest{
    WorkingDir: repoWorkingDir,
    TenantID:   tenantID,
    Cell:       "checkout", // empty or Self:true means current cell
})
if err != nil {
    return err
}
```

Supported values:

- empty `Cell` or `Self:true`: current cell from `.c2/config.yaml` or supported auto-detection
- `root`: configured or derivable root repo
- configured short names, such as `checkout`
- canonical repo strings, such as `github.com/acme/app`
- git URLs, such as `https://github.com/acme/app.git` or `git@github.com:acme/app.git`
- local paths, such as `./repo`, resolved relative to `WorkingDir`

The result includes:

```go
target.OriginalInput
target.ResolvedRepo
target.RepositorySource // normalized source for durable identity/filtering
target.DefaultRef
target.CellName
target.TenantID
target.Source           // self, config, repository, or local_path
```

## Submit A Recipe Job

Build the c2j start payload, then submit it through the caller-owned JobDB
engine/runtime:

```go
start, err := recipejob.BuildStartJob(recipejob.BuildStartJobRequest{
    TenantID: tenantID,
    Target:   target,
    Recipe:   "deploy", // empty defaults to "default"
    Inputs: map[string]interface{}{
        "prompt": prompt,
    },
})
if err != nil {
    return err
}

jobKey, err := starter.StartRecipeJob(ctx, start, engine)
if err != nil {
    return err
}
```

`BuildStartJob` fills the same durable fields as c2j submit:

- `Workflow.CellName`
- `Workflow.ProjectId`
- `GitBase.BaseRepo`
- `GitBase.BaseRef`
- `RecipeSource.Repo`
- `RecipeSource.Ref`
- `GitRef`
- `SubmittedAt`
- `InputHash`

If Cortex needs a caller-chosen job ID, pass `JobID` to
`BuildStartJobRequest`. `starter.StartRecipeJob` now forwards `start.JobID` when
no explicit `StartRecipeJobOptions.JobID` is supplied.

For prerequisites or other starter options, keep using:

```go
jobKey, err := starter.StartRecipeJobWithOptions(ctx, start, engine, starter.StartRecipeJobOptions{
    Prerequisites: prerequisites,
})
```

## List By Repo URL Or Short Name

For a repo URL:

```go
resp, err := recipejob.ListRecipeJobs(ctx, engine, recipejob.ListRecipeJobsRequest{
    TenantID:         tenantID,
    RepositorySource: "github.com/acme/app",
    PageSize:         50,
    PageToken:        pageToken,
})
```

For a configured short name, resolve it first and list by the normalized source:

```go
target, err := recipejob.ResolveTarget(ctx, recipejob.ResolveTargetRequest{
    WorkingDir: repoWorkingDir,
    TenantID:   tenantID,
    Cell:       "checkout",
})
if err != nil {
    return err
}

resp, err := recipejob.ListRecipeJobs(ctx, engine, recipejob.ListRecipeJobsRequest{
    TenantID:         tenantID,
    RepositorySource: target.RepositorySource,
    PageSize:         50,
})
```

`resp.Jobs` contains job ID, scheduler status, completion status/detail, store,
recipe name, repository source, cell name, git ref, JobDB timestamps, wait state,
and cancellation state.
`resp.NextPageToken` is the JobDB cursor for the next request.

To match c2j CLI's default visible list:

```go
statuses := recipejob.DefaultVisibleStatuses()
resp, err := recipejob.ListRecipeJobs(ctx, engine, recipejob.ListRecipeJobsRequest{
    TenantID:         tenantID,
    RepositorySource: target.RepositorySource,
    Statuses:         statuses,
    Stores:           recipejob.StoresForStatuses(statuses),
})
```

## Read One Recipe Job

```go
job, err := recipejob.GetRecipeJob(ctx, engine, recipejob.GetRecipeJobRequest{
    TenantID: tenantID,
    JobID:    jobID,
})
if errors.Is(err, recipejob.ErrJobNotFound) {
    return nil
}
```

## Clear Crash Concern

Clear the consecutive expired-lease counter on an existing job with operator
credentials:

```sh
c2j job clear-crash-concern JOB --reason 'worker issue fixed'
c2j job clear-crash-concern JOB --jobdb https://jobdb.example.com/tenant --json
c2j job clear-crash-concern JOB --embed
```

`reset-crash-concern` is an alias. The destination follows the usual `--jobdb`,
`C2J_JOBDB`, and project configuration precedence; `--embed` selects embedded
SQLite. `--worker-id` defaults to `c2j-cli` and identifies the operator in backend
tracing. `--reason` is optional. Remote calls use `JOBDB_BEARER_TOKEN` when set.
JSON success output is `{"tenant_id":"tenant","job_id":"JOB","cleared":true}`;
errors return nonzero without a success result.

Cortex can use its existing caller-owned runtime or the
`jobdbschema.WorkflowEngine` wrapper:

```go
err := recipejob.ClearCrashConcern(ctx, runtime, jobdb.ClearCrashConcernRequest{
    JobKey:   jobdb.JobKey{TenantId: tenantID, JobId: jobID},
    WorkerID: operatorID,
    Reason:   "worker issue fixed",
})
```

The helper accepts the narrow `recipejob.CrashConcernAdmin` interface. A
`jobdb.ConsumerRuntime` implements it directly. `jobdbschema.WorkflowEngine`
forwards through its `Registry` runtime. Retired goal lifecycle helpers return
unsupported errors before backend calls. The
upstream `workflow.Engine` interface alone does not expose this method; retain
the runtime or the c2j wrapper when wiring host management APIs.

The action uses tenant lifecycle authority, like cancellation. It is not granted
by task submission or inspection permissions and is not exposed through the task
helper. Delegated lease credentials are rejected by JobDB. Hosts should put it
behind the same operator access controls as their other job lifecycle actions.

Clearing preserves the job ID, retained history, payload, routing,
dependencies, availability, and execution deadlines. It does not acquire a lease
or execute the job; workers resume normal scheduling when eligible. This differs
from `c2j restart`, which creates a new continuation job. Success does not promise
`READY`, because other scheduling constraints still apply.

Use `errors.Is` with `jobdb.ErrInvalidCrashConcernRequest` for missing identity,
`jobdb.ErrJobNotFound` for a missing job in the selected tenant, and
`jobdb.ErrConflict` for a live lease or a completed, cancelled, or hidden job.
Authorization and transport errors also pass through. Retrying an already-clear
inactive job succeeds; retrying after a worker acquires it returns a conflict.

This c2j revision pins JobDB main commit `f92ae253a5da`
(`v0.0.75-0.20261106232324-f92ae253a5da`). Remote servers must include its
`ClearCrashConcern` endpoint/backend support; upgrading c2j alone does not add
the endpoint to an older server. Toy, SQLite, and PostgreSQL support the action.
The reason and worker ID are backend tracing context; SQLite and toy do not
persist those audit fields.

## Completion Results

`RecipeJob.CompletionStatus` copies JobDB's persisted final result in all list
and detail responses. `CompletionDetail` is loaded only by `GetRecipeJob` for
one selected job. Ordinary/child lists and CLI JSON leave it empty. For example,
a selected failed job can have these detail fields:

```json
{
  "job_id": "example-job",
  "status": "COMPLETED",
  "store": "ARCHIVED",
  "completion_status": "failed_system",
  "completion_detail": "container exited with status 1",
  "execution": {"status": "not_waiting", "source": "unavailable"}
}
```

| Field | Meaning |
| --- | --- |
| `status` | Scheduler state; `COMPLETED` alone does not establish success. |
| `completion_status` | Persisted terminal result: `success`, `failed_app`, `failed_system`, `failed_timeout`, or `cancelled`. Unknown future values pass through unchanged. |
| `completion_detail` | Original text from explicit inspection; never loaded by lists. |
| `execution.status` | Execution-requirement availability, such as `in_flight` or `not_waiting`; independent of completion. |

An empty completion status means unavailable, including when an older server or
record lacks it. Empty completion fields are omitted from JSON. Do not interpret
missing status as success, a failed earlier attempt as terminal failure, or
`cancel_requested` as completed cancellation. The projection never loads a recipe
or reads history/story data to infer missing completion fields.

Existing status filters, pagination, and active/archive selection are unchanged.
Select `jobdb.JobStatusCompleted` to include completed jobs; the default visible
statuses exclude them. The standalone [job listing API](pkg/joblist/README.md)
exposes the same fields.

## Execution Requirements

Set `BuildStartJobRequest.ExecutionRequirements` to a partial
`execution.Requirements` value for explicit submission overrides. It does not
describe the actual executor. The starter records resolved initial requirements
when supplied the root recipe; deferred submissions remain explicitly unresolved.

Lists make one JobDB call per page and preserve supplied completion detail,
full client payload, task coordinates, and prerequisites. These stored fields
need no per-row enrichment. Full `blocking_wait` remains unloaded in lists;
`blocking_wait_summary` exposes wait identity and deadline. Explicit selected
detail, inspection and stories retain complete waits and saved producer bindings.
Deprecated `other_goal_ids` is empty in every current c2j view; saved JSON still
decodes historical values. Current goal filters, queries and lifecycle helpers
return unsupported errors; they never broaden a request to an unfiltered list.
All application metadata is retained as `metadata`.

Waiting rows interpret supplied payloads locally. Missing payloads are stored
absence: c2j uses submission requirements when available, otherwise reports
`unresolved` with source `absent`. Published demand takes priority. Active rows
report `in_flight`; terminal/unknown rows report `not_waiting`. The old
`not_loaded` fallback has been removed; remote backends must implement the
[restored contract](MIGRATION-C2J-Restored-Job-List-Fields.md). See the
[Cortex UI guide](GUIDE-Cortex-Restored-Job-List-Fields.md) for display behavior.

`GetRecipeJob` explicitly reads one selected job's compact summary and `GetJob`
details. It requires a backend implementing both `ListJobs` and `GetJob`. Use it
for a selected attempt or job page, never for every row during list rendering.
`ExecutionViewFromInfo` in `pkg/joblist` also interprets loaded details directly.

`ListRecipeJobsRequest.ExecutionFilter` and
`ListChildRecipeJobsRequest.ExecutionFilter` are deprecated; non-nil filters
return `joblist.ErrExecutionDetailsRequired` without backend calls. For scheduling,
use the acquired lease's payload instead of adding a per-row detail loop.
See [the compact migration](MIGRATION-C2J-Compact-Job-Lists.md) and
[the restoration rollout requirements](MIGRATION-C2J-Restored-Job-List-Fields.md).

To execute jobs, use the separate
[`executionruntime` adapter](pkg/executionruntime/README.md) and compiler options.
See the [execution guide](GUIDE-Execution-Tracking.md) for the lifecycle.

## Compatibility Note

Current jobs use metadata for repository and submission identity, typed routes
for scheduling, and separate opaque client payload. No legacy-payload fallback
or stored-job migration is provided. Deploy with matching versions and fresh
format-3 storage as described in the [upgrade notes](JOBDB_UPGRADE_NOTES.md).

## Including mark-deleted jobs

`ListRecipeJobsRequest.IncludeHidden` and `ListChildRecipeJobsRequest.IncludeHidden`
default to false. Set true to include hidden and visible jobs within the existing
filters, including parent/invocation scope. Recipe builders preserve caller
statuses/stores without imposing the visible-status defaults of `joblist.Query`.
When supplied, those filters must also match hidden terminal jobs. This uses JobDB's existing `includeHidden` server contract;
it does not restore jobs or return purged records. Keep the option and other
filters fixed throughout pagination. The read-only `joblist.Query` exposes the
same boolean. No API is deprecated by this addition.

## Consumer-first exact-job interpretation

Ordinary goal-free creation and independent restart work against legacy JobDB
`v0.0.94-0.20261119021921-f885723cc03c`, before server goal retirement. Ordinary
create/restart and owner-lease child routes remain the backend interfaces. New
restarts explicitly send `ShareGoal: false`; omitted `starter.RestartOptions.Goal`
and the deprecated `"new"` spelling mean independent restart. `"continue"` rejects
before preparing or publishing. Restart leaves the source running and preserves
existing exact dependencies on it.

New ordinary recipe waits select the exact job and default to `complete`.
Result waits select that job and require `success`. Completion and soft inspection
retain failed/cancelled status and available partial output. A successful sibling
never repairs another job's dependency or replaces its artifact producer. Retired
goal targets, selection, query filters and wait modes fail explicitly. Opaque
application fields named `goal_id` remain data. Static admission also checks raw CEL
conditions on nodes, catches, state transitions and children, including shared
definitions. `recipe.ValidateJobConditions` applies that check to legacy execution
without changing its frozen wait policy; arbitrary application maps and quoted
CEL literals remain data.

Raw conditions use `jobsemantics.CheckExpression` on the complete CEL expression;
quoted `{{ ... }}` text never changes its grammar. Interpolated values use
`jobsemantics.CheckTemplate` instead, including expressions with quoted closing
delimiters. Both paths reject live goal access before fresh publication; saved
condition preflight applies before task effects for absent/1/2 snapshots. These
checks preserve quoted CEL literals and application data. Invalid raw CEL,
including interpolation supplied as a raw condition, rejects before effects.
`jobsemantics.CheckChildCondition` retains the child's supported raw-CEL and
interpolated forms while checking the complete raw expression first.

Fresh preparation persists `job_semantics_version: 2` in immutable start input and
carried context. Missing/1 remains legacy interpretation; it does not silently
receive current defaults. Unsupported implicit legacy goal waits reject before
incompatible effects. Explicit historical exact-job policies, captured includes,
command bytes, operation task contract 1, settled witnesses and artifact keys
remain authoritative. The chapter schema is v7, hash
`sha256:649def981380e3c981d0b8ddcd651ea06af5889a01a9885ca08710397c913532`.
Historical schema selectors are retained unchanged.

Use `starter.PrepareRecipeJob` followed by `starter.NewDescriptor` for durable
ordinary preparation. Persist the complete format-2 descriptor before its first
`Submit`. It reserves a destination ID, captures bytes and artifact digests, and
preserves nested JSON numbers when inserting the reserved ID, and checks current
interpretation and retired syntax before publication. The broker
exposes `childbroker.Prepare` and `SubmitPrepared`; CLI `--request-file` and
`--replay-request` support this ordinary path under the original authority.

For durable restart, call `starter.PrepareRestartJob(ctx, engine, source,
scopeID, patch, starter.RestartOptions{RequestID: stableID})`, persist the complete
returned `jobdb.SubmitRestartJobRequest`, then call
`starter.ReplayRestartJob(ctx, submitter, savedRequest)`. Keep the original request
ID, destination when supplied, source, sharing mode, boundary and ordered patch.
Do not prepare again from later editor/source state after uncertainty. Reacquire
only current credentials for the same authorized operation/parent. Receipts must
match the frozen tenant/job/cell and any supplied repository assertion, using
normalized repository identity. An omitted historical repository assertion stays
optional.

Legacy format-1 selected descriptors and frozen restart requests replay through
their original idempotent endpoints unchanged. This follows the human direction
that request IDs satisfy recovery. There is no new receipt-only API or dependency.
These legacy endpoints can create the original operation on a miss; this is not
a read-only lookup guarantee. A changed semantic request conflicts. Unsupported,
missing, denied or malformed responses remain errors without dropping mode or
switching to an ordinary/new operation.

The current SDK pin `28ba72a3ea5a` refuses these historical modes against legacy
servers without its new capability route, even for committed receipts. Owner
hosts can configure the c2j recovery boundary using the **same** runtime URL and
authenticated HTTP client as their ordinary owner connection:

```go
recovery, err := jobrecovery.New(runtimeURL, ownerHTTPClient)
// Handle err before constructing the wrapper.
engine := jobdbschema.WorkflowEngine{
    Engine: nativeEngine, Registry: runtime, Recovery: recovery,
}
```

Import `github.com/colony-2/c2j/pkg/jobrecovery`. The CLI remote runtime configures
this connection automatically. Pass this wrapper to starter replay, or forward
`jobref.HistoricalRecovery` through any host submitter wrapper. Only format-1
selected descriptors and frozen
sharing restarts select `jobref.HistoricalRecovery`; fresh create and independent
restart keep their normal SDK paths. Without configuration the SDK's safe refusal
still applies. The adapter uses the original primary-selection discovery and
protected PUT endpoint, or original explicit/request-ID restart endpoint. It
preserves the complete frozen body and validates the returned identity, including
repository assertions. It does not advertise new capabilities, prepare a schema,
change the SDK pin, follow redirects or retry through another endpoint.

This adapter is owner-scoped. Never attach it to a lease/task/delegated submitter
or substitute stronger credentials; restricted requests retain their original
restricted endpoint and authority. The legacy selected endpoint itself rejects
lease tokens. No public JobDB compatibility option is added: the cancelled option
is separate from c2j's required historical reconciliation. Positive committed
receipt tests run a separate server/SQLite backend built from JobDB
`f885723cc03c` in [the pinned fixture](testdata/exactjob-legacy/README.md).

Source delivery does not upgrade deployed workers or retire server goals. Cortex
should pin the immutable c2j commit produced by managed publication; this worktree
has no new published c2j revision yet. The PostgreSQL harness in
[testdata/exactjob-postgres](testdata/exactjob-postgres/README.md) records the checked
legacy compatibility matrix and current concurrent-creation limitation. Full
acceptance and an external published-module host check remain required before
claiming release completion.
