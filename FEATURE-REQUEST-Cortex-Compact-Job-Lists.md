# Cortex: render compact job lists and inspect selected attempts

## Problem and required outcome

The original audit below predates the restored stored-field projection.
Current lists retain opaque payloads, task coordinates and completion detail; see
[the current guide](GUIDE-Cortex-Restored-Job-List-Fields.md). Lists carry wait
summaries; explicit inspection loads complete waits. Deprecated secondary IDs
stay empty in current list/detail/story views. Backend goal queries retain
historical members whose primary differs. Cortex must render supplied row fields
and load complete waits only for a selected/expanded attempt.
Opening or refreshing a collapsed list must issue one JobDB list operation per
page, with zero per-job inspection operations.

This request is based on Cortex main
[`e7bfa71bcd91c9cfa5dbc8ce61874624d2ce71bf`](https://github.com/colony-2/cortex/tree/e7bfa71bcd91c9cfa5dbc8ce61874624d2ce71bf),
audited on 2026-10-28. See [c2j migration](MIGRATION-C2J-Compact-Job-Lists.md).

## Historical audit findings (2026-10-28)

| Source | Behavior at audit | Recommendation at audit |
| --- | --- | --- |
| `server/internal/cortex/handlers.go:handleListJobs` | One `recipejob.ListRecipeJobs` call; no detail loop | Preserve this path and backend pagination. Never hydrate results in the handler. |
| `handlers.go:handleGetJob` | Calls `recipejob.GetRecipeJob` | Keep as selected inspection; upgraded c2j reads one summary and one detail. |
| `handlers.go:requireJob` | Uses `GetRecipeJob` just to check existence | Use a compact keyed list/existence helper if no details are consumed, avoiding the newly explicit detail fetch. |
| `web/app/src/components/JobsListPage.tsx` | Groups on primary and `other_goal_ids`; renders completion text and dependency links from rows | Group unfiltered rows only by primary goal. Use query goal context on goal-specific pages; backend goal filtering includes additional members. Remove list-only detail assumptions. |
| `GoalJobOverview.tsx` | Passes a selected list object directly into `JobListDetails`; renders progress for every attempt | Fetch the selected attempt through `getJob`, not every attempt. Keep the attempt table compact. |
| `JobWaitLinks.tsx` | Derives dependency-target links from `blocking_wait` or `wait_for` | Render a generic dependency-wait indicator from `blocking_wait_summary` / status on compact rows. Show target links only after inspection. Input/review links can still use `next_route`. |
| `JobListDetails.tsx` | Reads completion detail, dependency targets, execution demand, and task coordinates | Render loaded inspection data with loading/error states, rather than treating absent list values as empty details. |
| `JobStoryPage.tsx` | Already calls `getJob` for the selected job | Preserve selected detail fetching; do not introduce a list-wide prefetch. |
| `web/shared/src/types.ts` | One `RecipeJob` interface mixes summary and detail fields | Add the compact wait summary and distinguish list summaries from inspected details so absence cannot imply authoritative emptiness. |

## Implementation requirements

1. Bump c2j and align the backend with the restored stored-field list contract
   in [the current guide](GUIDE-Cortex-Restored-Job-List-Fields.md).
2. Retain title/prompt, cell/repository labels, primary goal, parent, completion
   status/detail, timestamps, route, and cancellation in the ordinary list.
   Preserve all application metadata, schema hash, opaque client payload and
   paired revision, task coordinates, ordered prerequisites and c2j's interpreted
   execution view. Render these supplied fields without inspection reads.
3. Add `blocking_wait_summary?: { waitId: string; deadline?: string }`. A
   summary says a stored wait is blocking; it cannot identify pending targets.
   Do not infer pending targets or absence of dependencies from an unloaded full
   wait. Secondary IDs are deprecated and empty even after inspection; use
   backend goal queries for historical membership. Preserve unknown completion
   status values.
4. When complete dependency explanations are requested, inspect only the selected
   attempt. Expanding stored row fields needs no detail request. On attempt change,
   prevent late inspection responses from replacing another selection.
   Scope caches by tenant and job, retain loading/error/retry behavior, and
   refresh visible details deliberately. Payload revision alone is not a cache
   version for completion/dependency changes.
5. Keep complete-wait loading state separate from stored row fields. Refresh the
   supplied completion, payload, coordinates and execution view from list rows;
   an unloaded full wait must not erase inspected dependencies. Refresh visible
   wait details deliberately so old explanations do not become stale list facts.
6. Do not restore secondary membership grouping with one `GetJob` per row.
   Primary-goal grouping and server-side goal filtering are sufficient for the
   compact list. Authoritative historical membership comes from backend goal
   queries; selected-job inspection does not restore secondary IDs.

## Where the removed loop could reappear

Do not add `Promise.all(page.jobs.map(getJob))`, a server-side `GetRecipeJob`
loop, eager hooks on every collapsed row, or requests for all attempts when a
single attempt is selected. Likewise do not call pending-input discovery to
hydrate job rows: it intentionally reads form data for each input occurrence.
A selected panel may fetch one job; the cost must scale with explicit visible
inspections, not the number of list rows.

## Acceptance tests

- Instrument the backend: list/refresh/next page of 100–200 collapsed jobs yields
  one `ListJobs` and zero `GetJob`, `GetJobRun`, goal, wait, or artifact reads.
- List fixtures supply completion detail, task coordinates, ordered prerequisites,
  opaque payload and paired revision, execution view and wait summary. Assert
  exact multiline/Unicode completion text, unfamiliar statuses, payload absence,
  null, arrays and large integers survive. These fields render without detail
  reads; labels, pagination, parent and input/review links remain. An empty page
  with a cursor remains navigable.
- Render execution demand from the supplied c2j view, preserving unresolved and
  invalid-demand states. Refresh waiting rows through active and terminal states
  and assert stale demand clears without extra reads.
- Requesting complete dependency explanations fetches only the selected attempt;
  choosing another fetches only that attempt. Cover close/reopen, refresh, errors,
  rapid selection, tenant switches, and persisted expanded state in the URL.
  Unloaded full waits have loading/error states, not an authoritative “none”.
- Goal-specific filtering finds additional members without exposing or fetching
  all their other memberships. Unfiltered grouping uses the primary goal only;
  deprecated secondary IDs stay empty in both list and inspected responses.
- Update `JobsListPage.test.tsx` and `web/app/tests/goals.spec.ts` with separate
  list and selected-job responses and request-count assertions. List fixtures
  retain restored stored fields and omit complete evaluated waits; selected-job
  fixtures add complete waits and saved producer bindings.

Coordinate rollout with the c2j migration. Merely bumping the dependency leaves
expanded panels incomplete and changes grouping silently.
