# Migrate to compact job lists

`ListJobs` and `POST /v1/tenants/{tenantId}/jobs/query` now return compact
summaries by default. There is no request flag and no legacy-detail mode.

The corrected contract preserves stored row fields and excludes evaluated
dependency explanations and additional goal memberships. Completion detail,
full client payload, execution state, and prerequisites have been restored;
they are no longer detail-only. See the
[c2j restoration migration](MIGRATION-C2J-Restored-Job-List-Fields.md).
`BlockingWait` and `OtherGoalIDs` remain unloaded on ordinary lists; their empty
values do not establish that no dependencies or additional memberships exist.

## What lists retain

- Job key/type, primary goal ID, parent ID, schema hash.
- Status and persisted completion status/detail, including unfamiliar completion
  values and exact detail text.
- Stored next route, cancellation flag, creation/archive/availability/expiry/lease
  expiry timestamps where supported by the backend.
- Full opaque client payload and its paired revision, preserving JSON null,
  arrays, unrelated application namespaces, and large integers.
- Stored execution state (run policy and task coordinates) and ordered
  prerequisite IDs. Coordinates are not task input/output data.
- **All application metadata**, preserving existing metadata filtering and UI
  labels. Runtime-private metadata remains excluded as before.
- `BlockingWaitSummary` / `blockingWaitSummary`, containing `waitId` and an
  optional `deadline`. This reads persisted pending wait state, projecting an
  elapsed deadline as no longer blocking. It does not evaluate target goals,
  enumerate candidates, or explain why a dependency is pending.

Status still uses scheduler readiness semantics. Filtering by status, available
routes, additional goal membership, metadata, tenant, job key, parent, creation
time, store, and visibility still happens before selecting the returned page.
The returned `nextRoute` remains the stored route even when an alternate is due.

## Move detail reads to selection/inspection

| Deprecated list field | Replacement |
| --- | --- |
| `BlockingWait` / `blockingWait` | `GetJob.BlockingWait`, or `GetDependencyWait` using the job key and `BlockingWaitSummary.WaitID` |
| `OtherGoalIDs` / `otherGoalIds` | `GetJob.OtherGoalIDs` |

`GetJob` now also exposes `CompletionStatus`, `CompletionDetail`, `WaitFor`, and
`NextRoute`. HTTP names are `completionStatus`, `completionDetail`, `waitFor`,
and `nextRoute`. The task route and task coordinates can therefore be read from
one detail response. Completed-job detail reads can also load stored results;
use the dependency-wait endpoint when only wait details are needed.

```go
page, err := runtime.ListJobs(ctx, jobdb.ListJobsRequest{
    TenantIds: []string{tenant}, PageSize: 100,
})
// Render stored list fields directly, including payload and completion detail.
// On selection, fetch evaluated waits or additional memberships if needed:
info, err := runtime.GetJob(ctx, selectedJobKey)
// Use info.BlockingWait and info.OtherGoalIDs.
```

Do not automatically call `GetJob` for every row on every list refresh. Fetch
only selected/expanded jobs, or for an explicit operation needing their details.
Task discovery is such an operation: JobDB's workflow SDK now fetches detail
coordinates for the tasks it returns. Task completion remains guarded by the
recorded input hash/ordinals and route, so a stale handle cannot complete newer
work. Job-run inspection and worker run outcomes likewise obtain their required
details explicitly.

`otherGoalIds` remains unloaded, and full `blockingWait` is omitted. The restored
`waitFor`, `executionState`, `completionDetail`, and `clientPayload` fields reflect
stored values using their existing encodings. Missing payloads mean stored
absence on supported backends; c2j no longer substitutes `not_loaded`.

This change is scoped to ordinary job listing. `GetJob`, `GetJobRun`, dependency
inspection, and schedule-run inspection continue exposing their existing
inspection details. `JobSummary` is also embedded in schedule responses, so its
Go deprecation annotations must not be mistaken for removal from those APIs.

## Backend rollout

SQLite filters, sorts, and paginates in SQL with one query per page. Pure SQL
callbacks preserve the existing metadata equality, encoded prerequisite IDs,
and nanosecond deadline semantics. There are no per-result enrichment queries.
Selective metadata/status filters can still require examining many candidates;
this is not a promise of constant total query work. Large application metadata
also remains a response-size cost by request.

The core runtime exposes the compact projection, but an external scheduler can
still do unnecessary work before returning it. Update pgjobdb following the
[restoration migration](MIGRATION-C2J-Restored-Job-List-Fields.md). c2j requires
the restored remote projection; do not add per-row reads to compensate for
an older backend.
