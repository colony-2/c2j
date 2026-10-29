# Restore stored job-list fields in Cortex

c2j again preserves completion detail, the full client payload and revision,
task coordinates, and prerequisite IDs in ordinary job lists. Restore their
display from the existing list response. A page still makes one JobDB
`ListJobs` call; rendering these fields must not introduce per-row detail reads.

## Dependencies and response mapping

Update Cortex to a c2j revision containing this restoration. c2j pins JobDB
`v0.0.64-0.20261029043940-9c44e3e3bfa1`. Remote JobDB/pgjobdb servers must also
implement the restored contract; updating Cortex alone does not update them.
See [the backend migration](MIGRATION-C2J-Restored-Job-List-Fields.md).

Audit backend response adapters, frontend response types, client caches, and row
components for fields discarded during the compact-list migration. The names
below are **c2j JSON names**, not the camelCase names of raw JobDB responses.
Use the same mapping for ordinary, archived, child-job, and keyed lists.

| c2j field | Cortex behavior |
| --- | --- |
| `completion_status` | Preserve unfamiliar strings. Keep terminal outcome separate from scheduler `status`; a retrying job need not have a terminal result. |
| `completion_detail` | Restore completion/error text in the row's existing message or expanded-message area. Preserve Unicode, whitespace, and line breaks; render as text, not HTML. Empty detail is valid. Visual truncation is fine if the full text remains expandable/copyable from the same response. |
| `execution` | Restore requirements badges/details using c2j's interpreted view, as described below. |
| `client_payload`, `client_payload_revision` | Retain the complete opaque value and its paired revision in the client model. Do not replace it with only `c2j.execution`, prune other namespaces, or fetch it again per row. |
| `task_wait` | Retain `inputOrdinal`, `outputOrdinal`, `inputHash`, and `resumeJobType` for existing task UI. These identify an occurrence; they are not input/output task data. |
| `wait_for` | Restore prerequisite IDs in their supplied order. Link using the current job's tenant/project. Do not fetch target jobs just to populate row labels. |
| `blocking_wait_summary` | Retain `waitId` and optional `deadline`. Use them for the existing wait display and explicit selected-wait inspection. |
| `next_route` | Continue using the stored route for routing display. Do not infer a current task from archived coordinates alone. |

Workflow summaries also preserve `ExecutionState.RunPolicy` and `TaskWait`.
c2j's recipe/CLI list DTOs expose coordinates as `task_wait`; do not assume a new
`execution_state` field exists on those DTOs.

## Render execution requirements

Use the returned `execution` view rather than reimplementing demand selection
from metadata or the opaque payload in the browser.

| `execution.status` | Display behavior |
| --- | --- |
| `specified` | Display requirements from `execution.demand.effective`, retaining existing formatting for resources and environment requirements. |
| `unspecified` | The recipe's requirements are resolved but empty. Display the existing “no requirements specified” state. |
| `unresolved` | Requirements are not resolved. Display an unknown/pending-requirements state, not “no requirements.” |
| `malformed`, `unsupported` | Preserve `execution.diagnostic` and display the existing invalid/unsupported requirements state. Do not silently substitute defaults. |
| `in_flight` | The job is active; this list view does not expose current demand. Do not keep stale requirement badges from an earlier waiting row. |
| `not_waiting` | The job is terminal or otherwise not waiting for execution. Clear waiting-demand UI. |

c2j has removed the missing-payload `not_loaded` fallback. A missing payload is
stored absence: submission requirements apply when available; otherwise the view
is `unresolved` with source `absent`. An empty resolved submission produces
`unspecified`. A published demand takes priority and has source `yield`;
submission-derived demand has source `submission`. Do not treat payload revision
zero as proof of absence, or a nonzero revision as proof of current demand:
payload resets can leave a nonzero revision.

Absence and a present JSON `null` remain distinct in the raw response. Let c2j
validate present payloads and interpret their demand; do not turn arrays or
malformed execution namespaces into empty objects. Replace the row's execution
view on refresh so transitions between waiting, active, and terminal states
clear stale badges and diagnostics.

## Preserve the list boundary

- Keep one backend page per Cortex page and pass its continuation unchanged.
  Do not scan extra pages to fill a locally filtered page. c2j's compatibility
  list filters remain rejected; this restoration does not re-enable them.
- Do not call `GetJob`, `GetJobRun`, artifact/task APIs, goal-membership APIs,
  or dependency-explanation APIs for every row on refresh or expansion merely
  to show these restored fields.
- `other_goal_ids` and full `blocking_wait` remain unloaded. Empty values do not
  prove that no extra memberships or dependency explanations exist. Load those
  only during explicit selected-job/wait inspection.
- Opening an input form or taking a task action still requires the existing
  fresh task-handle lookup and stale-occurrence protection. List coordinates
  do not replace those checks.
- Keep payload and revision together when updating cached rows. Preserve large
  integers such as `9007199254740993`: ordinary JavaScript JSON number parsing
  cannot round-trip them exactly. If the UI exposes raw payload viewing/editing,
  retain raw JSON or use a lossless parser; do not rewrite the opaque payload
  from a normal parsed JavaScript object. Payloads can be 65,536 bytes.

## Acceptance checks

1. Verify ordinary, archived, child, and keyed rows render exact completion text,
   unfamiliar completion statuses, empty details, and retries without a terminal
   completion. Check text escaping and multiline/Unicode display.
2. Exercise payload absence, explicit null, arrays, unrelated namespaces, a
   large integer, and a 65,536-byte payload. Confirm the model retains payload
   and revision together without pruning or precision loss.
3. Check published demand overrides submission requirements. Check missing
   payloads with and without submission metadata, including nonzero revisions;
   then transition the same row to active and completed and verify stale demand
   disappears. Preserve invalid-demand diagnostics.
4. Verify every task coordinate, prerequisite ordering, wait ID/deadline, and
   archived coordinates survive adapters. Keep task data and evaluated wait
   explanations behind explicit inspection.
5. Instrument pages of 1 and 200 rows: one backend list call, unchanged
   continuation, and no hydration calls. Verify that already-supplied fields
   render immediately without a detail request or a perpetual loading state.

The c2j restoration tests cover SQLite and an HTTP-wrapped runtime, JSON
projections, CLI parity, and the one-page rule. Cortex should add UI/adapter
coverage and verify its deployed pgjobdb endpoint separately.
