# Cortex Recipe Job API Guide

This guide covers the public c2j surface added for Cortex-style hosts that use
remote JobDB and must not import `cmd/c2j/internal/*`.

The package is:

```go
import "github.com/colony-2/c2j/pkg/recipejob"
```

## Contract

New c2j recipe jobs persist the target repository in JobDB metadata:

- metadata JSON field: `repo`
- Go field: `starter.JobMetadata.RepositorySource`
- field constant: `starter.MetaFieldRepo`

The value is the normalized repository source used by c2j submit semantics, for
example `github.com/acme/app` becomes `https://github.com/acme/app.git`.

Submitted nonblank string `inputs.title` is stored as optional metadata `title`
(`starter.JobMetadata.Title`, `starter.MetaFieldTitle`). `RecipeJob.Title` and
`joblist.Job.Title` expose it as optional JSON `title`, alongside the unchanged
`prompt` and `goal_id`. List, single-job, child-job, and goal-candidate projections
use the metadata already present on the summary, with no additional reads.
Stored titles are preserved verbatim; missing or empty titles are omitted.
Cortex owns the nonblank `title → prompt → goal_id` display fallback. Submission
does not index whitespace-only titles, non-string values, or secret references.
CLI users can populate the input with `c2j submit "prompt" --title "Job title"`.

The public `pkg/recipejob` package exposes:

- target resolution with c2j CLI-compatible cell rules
- start-request assembly for `workflowctl.StartJob`
- recipe-job list/read helpers backed by JobDB `ListJobs`
- reusable c2j list defaults for visible statuses and stores

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
need no per-row enrichment. Evaluated dependency explanations and additional
goal memberships remain unloaded. `blocking_wait_summary` exposes wait identity
and deadline; all application metadata is retained as `metadata`.

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
