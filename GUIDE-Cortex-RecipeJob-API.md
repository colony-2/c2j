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

- empty `Cell` or `Self:true`: current cell from `.c2j/config.yaml` or supported auto-detection
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

`resp.Jobs` contains job ID, status, store, recipe name, repository source, cell
name, git ref, JobDB timestamps, wait state, and cancellation state.
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

## Execution Requirements

Set `BuildStartJobRequest.ExecutionRequirements` to a partial
`execution.Requirements` value for explicit submission overrides. It does not
describe the actual executor. The starter records resolved initial requirements
when supplied the root recipe; deferred submissions remain explicitly unresolved.

`RecipeJob.Execution` exposes the submission/latest-yield view without resolving
recipes or reading history for waiting jobs only. Active jobs report `in_flight`
without requirements; terminal/unknown states report `not_waiting`. Neither
state matches compatibility filters, including `IncludeUnresolved`.
`ClientPayload` and `ClientPayloadRevision` remain
separate from submission metadata. The current execution namespace takes
precedence over the initial hint; the historical handoff allocation is not a
live usage record.

Set `ListRecipeJobsRequest.ExecutionFilter` or
`ListChildRecipeJobsRequest.ExecutionFilter` to `*execution.Filter` to opt into
compatibility filtering. Supply a version-1 `Allocation` containing actual
candidate facts. Unknown facts cannot satisfy explicit requirements.
`IncludeUnresolved` admits bootstrap candidates while checking known overrides.
A nil filter leaves the list unfiltered, including jobs with diagnostic state.

Filtered pages scan underlying pages to fill the requested number of matches.
Reuse the returned token with the same filters. Malformed/unsupported execution
state produces `*recipejob.ExecutionDemandError`, not an unconstrained match.
Filtering is advisory and does not reserve or acquire work.

To execute jobs, use the separate
[`executionruntime` adapter](pkg/executionruntime/README.md) and compiler options.
See the [execution guide](GUIDE-Execution-Tracking.md) for the lifecycle.

## Compatibility Note

Current jobs use metadata for repository and submission identity, typed routes
for scheduling, and separate opaque client payload. No legacy-payload fallback
or stored-job migration is provided. Deploy with matching versions and fresh
format-3 storage as described in the [upgrade notes](JOBDB_UPGRADE_NOTES.md).
