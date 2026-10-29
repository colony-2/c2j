# c2j compact job list migration

c2j now pins JobDB main `9c44e3e3bfa1` (`v0.0.64-0.20261029043940-9c44e3e3bfa1`).
The c2j list path covers `pkg/joblist`,
`pkg/recipejob`, ordinary/child CLI JSON, goal candidate lists, and workflow
control listing. The corrected boundary preserves stored fields and removes
extra reads/evaluation. The initial migration over-pruned completion detail,
client payload, task coordinates, and prerequisites. c2j now passes those
fields through, and the pinned JobDB restores them. See the
[restoration migration](MIGRATION-C2J-Restored-Job-List-Fields.md).

## Cost and response contract

An ordinary page makes one JobDB `ListJobs` call. There are no per-row `GetJob`,
`GetJobRun`, dependency, goal, artifact, or task reads, and no extra scans to fill
a logical page. Backend filters, page size, and continuation pass through.
Explicit `--all` and child-context collection still traverse requested pages.

Rows retain job identity/type, primary goal, parent, schema hash, status,
completion status/detail (including unknown status values), stored route and
task coordinates, prerequisite IDs, cancellation, timestamps, full opaque client
payload and revision, and all application metadata when supplied by JobDB. c2j's
recipe/title/prompt/cell/repository labels remain derived from that metadata.
`blocking_wait_summary` contains `waitId` and optional `deadline`, without target
resolution. Additional goal membership still works as a backend filter.

The pinned backend returns completion detail, client payload, execution state,
and prerequisites. Remote endpoints also need the matching JobDB/pgjobdb
restoration. c2j does not fetch missing fields with per-row reads.

The following fields still require explicit inspection. Empty means **not loaded**:

| List field | Explicit replacement |
| --- | --- |
| `blocking_wait` | `GetJob.BlockingWait` or `GetDependencyWait` using the summary wait ID |
| `other_goal_ids` | `GetJob.OtherGoalIDs` |
| `workflowctl.JobItem.TaskData` | `GetWaitingTask(...).Data()`; listed TaskData is nil |

`recipejob.GetRecipeJob` is now an explicit inspection operation: one compact
query for metadata/timestamps and one `GetJob` for the selected job's details.
Its backend must implement both methods. JobDB `JobInfo` does not contain the
metadata/timestamps needed to eliminate that selected-job summary query. These
reads are not an atomic snapshot. Do not call this helper for every listed row.
Use a compact keyed list instead for existence checks that need no details.

## Execution demand

When a list supplies a client payload, c2j interprets waiting-job demand locally
with the usual `submission`, `yield`, and `absent` semantics. No extra read is
needed. Active jobs remain `in_flight`; terminal/unknown jobs remain `not_waiting`.
The full payload is preserved regardless of execution status.

Missing payloads now mean stored absence, regardless of payload revision.
Waiting jobs use submission requirements when available, otherwise report
`unresolved` with source `absent`. Published demand takes priority. The old
`not_loaded` fallback is removed; supported remote endpoints must implement the
restored list contract.

A non-nil `ExecutionFilter` and CLI `--compatible-with-execution` remain deprecated
and rejected with `joblist.ErrExecutionDetailsRequired` before list dispatch.
`IncludeUnresolved` cannot override this. Re-enabling them requires
pagination semantics that do not scan additional
backend pages to fill one filtered page. Interpreting supplied payloads itself
adds no query.

For an explicitly selected job, evaluate
`joblist.ExecutionViewFromInfo(summary.Metadata, info)` and then
`execution.Filter.Match`. For scheduling, prefer
`execution.Inspect(metadata, lease.ClientPayload())` after bounded admission and
lease acquisition. This is current data the claim already returns; it requires
no discovery enrichment loop.

## Audited c2j consumers

- Ordinary/child recipe lists, `joblist.Client.List`, goal candidates, and
  workflow control remain compact. A 200-row regression fixture rejects every
  non-list operation and checks a single list call.
- `GetRecipeJob` explicitly loads selected details; embedded and HTTP tests
  cover persisted completion detail. Metadata remains available without detail
  fetches for title/prompt projections.
- Pending-input discovery already calls `GetWaitingTask` and reads form data for
  each returned occurrence. It uses the current handle's coordinates to identify
  the occurrence even when list coordinates are available. This existing
  specialized inspection loop remains;
  **do not use it to enrich the ordinary jobs list or add per-row input badges**.
  Completed inputs are skipped; a replacement input uses its newly loaded
  occurrence identity. Submission retains task-handle stale-work protection.
- Story summary listing uses metadata and timestamps, without new JobDB reads.
  Story/detail inspection already requests job runs separately.
- Started-child context and restart submission use retained identity/metadata.
  Restart intentionally uses the original submission hint, not current demand.

## Downstream audit and rollout

The original downstream audit used these revisions on 2026-10-28; the storage
correction above was audited on 2026-10-29:

- Cortex main `e7bfa71bcd91c9cfa5dbc8ce61874624d2ce71bf`: ordinary handlers already
  delegate directly to c2j. Completion detail, payload, task coordinates, and
  prerequisites can remain in lists once the backend restores them. Additional
  memberships and evaluated dependency explanations still need selected-job
  inspection. Do not add per-row detail requests to render list rows.
- Pulse main `071cf32f1ef5ae6e31644d051a85c3e6e3efe022`: discovery is one list call,
  but allocation is calculated before admission. With the current backend,
  acquire leases before determining allocation requirements; the lease already
  supplies current payload. This changes ordering, not intended allocation or
  execution behavior. Restored list payloads remove the missing-data reason for
  requiring this ordering change. The lease remains authoritative before
  execution in either case. A per-row `GetJob` would move the unwanted loop up.

c2j no longer emits `not_loaded` for missing list payloads. Supported backends
must return the restored projection; lease state remains authoritative for
execution. No downstream source changes are included here. See the
[restoration migration](MIGRATION-C2J-Restored-Job-List-Fields.md) for the
corrected Cortex/Pulse expectations and rollout requirements.

An external scheduler backend can still do enrichment internally. c2j's tests
prove its own call count; backend SQL/query-count tests must independently cover
the JobDB and pgjobdb restoration.
