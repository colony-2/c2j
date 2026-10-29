# c2j compact job list migration

c2j now pins JobDB main `0bd5f2c1c456` (`v0.0.62-0.20261028213659-0bd5f2c1c456`).
This propagates JobDB's compact list contract through `pkg/joblist`,
`pkg/recipejob`, ordinary/child CLI JSON, goal candidate lists, and workflow
control listing. It deliberately does not recreate removed enrichment in c2j.

## Cost and response contract

An ordinary page makes one JobDB `ListJobs` call. There are no per-row `GetJob`,
`GetJobRun`, dependency, goal, artifact, or task reads, and no extra scans to fill
a logical page. Backend filters, page size, and continuation pass through.
Explicit `--all` and child-context collection still traverse requested pages.

Rows retain job identity/type, primary goal, parent, schema hash, status,
completion status (including unknown values), stored route, cancellation,
timestamps, client payload revision, and all application metadata. c2j's
recipe/title/prompt/cell/repository labels remain derived from that metadata.
`blocking_wait_summary` contains `waitId` and optional `deadline`, without target
resolution. Additional goal membership still works as a backend filter.

The following retained compatibility fields are empty on lists, even against
older servers that still send details. Empty means **not loaded**:

| List field | Explicit replacement |
| --- | --- |
| `blocking_wait` | `GetJob.BlockingWait` or `GetDependencyWait` using the summary wait ID |
| `other_goal_ids` | `GetJob.OtherGoalIDs` |
| `completion_detail` | `GetJob.CompletionDetail` |
| `wait_for` | `GetJob.WaitFor` |
| `client_payload` | `GetJob.ClientPayload` or the acquired lease payload |
| `task_wait` / `ExecutionState` | `GetJob.ExecutionState` or `GetWaitingTask` |
| `workflowctl.JobItem.TaskData` | `GetWaitingTask(...).Data()`; listed TaskData is nil |

`recipejob.GetRecipeJob` is now an explicit inspection operation: one compact
query for metadata/timestamps and one `GetJob` for the selected job's details.
Its backend must implement both methods. JobDB `JobInfo` does not contain the
metadata/timestamps needed to eliminate that selected-job summary query. These
reads are not an atomic snapshot. Do not call this helper for every listed row.
Use a compact keyed list instead for existence checks that need no details.

## Execution demand

Waiting rows report `Execution.Status = "not_loaded"`, `Source = "unavailable"`,
no `Demand`, and `Published = false` (not inspected). `Initial`, when present,
is only an immutable submission hint from metadata. Active jobs remain
`in_flight`; terminal/unknown jobs remain `not_waiting`. A missing list payload
must never imply that a job has no published demand or that initial requirements
are current, including at payload revision zero.

A non-nil `ExecutionFilter` and CLI `--compatible-with-execution` are deprecated
and rejected with `joblist.ErrExecutionDetailsRequired` before list dispatch.
`IncludeUnresolved` cannot override this. This is an intentional behavioral
change: automatically fetching payloads or silently matching old hints would
violate either the list cost contract or placement correctness.

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
  each returned occurrence. It now uses the handle's coordinates rather than
  omitted list coordinates. This existing specialized inspection loop remains;
  **do not use it to enrich the ordinary jobs list or add per-row input badges**.
  Completed inputs are skipped; a replacement input uses its newly loaded
  occurrence identity. Submission retains task-handle stale-work protection.
- Story summary listing uses metadata and timestamps, without new JobDB reads.
  Story/detail inspection already requests job runs separately.
- Started-child context and restart submission use retained identity/metadata.
  Restart intentionally uses the original submission hint, not current demand.

## Downstream audit and rollout

Audited on 2026-10-28:

- Cortex main `e7bfa71bcd91c9cfa5dbc8ce61874624d2ce71bf`: ordinary handlers already
  delegate directly to c2j. The UI assumes list rows carry detail and additional
  goal memberships. Move detail to selected attempts; keep collapsed rows
  compact. See [Cortex feature request](FEATURE-REQUEST-Cortex-Compact-Job-Lists.md).
- Pulse main `071cf32f1ef5ae6e31644d051a85c3e6e3efe022`: discovery is one list call,
  but the controller currently calculates allocation for each row before
  admission. A per-row `GetJob` there would simply move the old enrichment loop
  upward. The existing bounded claim/prepare phase already gets current demand
  from the lease before provider submission. Remove its dependency on full
  pre-claim demand. See [Pulse feature request](FEATURE-REQUEST-Pulse-Compact-Job-Lists.md).

Both repositories need coordinated updates. Old Pulse rejects `not_loaded` in
`Job.Allocation`, so bumping its c2j dependency alone would stop scheduling.
Old Cortex loses expanded details and secondary-membership grouping unless its
UI is migrated. No downstream source changes are included in this c2j change.

An external scheduler backend can still do enrichment internally. This c2j
change does not certify pgjobdb's query cost; verify its compact-list migration
separately. SQLite's compact path is supplied by the pinned JobDB change.
