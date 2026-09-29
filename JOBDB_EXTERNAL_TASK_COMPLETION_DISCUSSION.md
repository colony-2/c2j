# External task completion: persistence and scheduler failure window

Status: deferred investigation, separate from native review design. No solution
has been selected. This issue is not a prerequisite for continuing the review
contract discussion or a reason to introduce review-specific persistence.

## Observation

The c2j checkout pins JobDB to
`v0.0.19-0.20260919034646-71b6668a65db` in `go.mod`.
In that version, an externally completed task can retain a durable output while
remaining scheduled as waiting if the scheduler update fails after the output is
written. An identical completion retry then encounters the existing output.

This concerns the JobDB task-completion API independently of any HTTP transport,
review schema, or c2j user interface.

In JobDB's `pkg/jobdb/runtime/sqlite/runtime.go`,
`CompleteTaskIfWaiting` performs these operations:

1. Read and check the waiting task and job state.
2. Validate the proposed output and its chapter ordinal.
3. Save the output chapter and associated artifacts (`SaveChapter`, around
   line 1197 in the pinned version).
4. Open a separate scheduler transaction (`withTx`, around line 1218), recheck
   state, and update the job route to resume recipe execution.

The scheduler transaction cannot roll back the previously saved chapter.
On retry, `ensureNextVisibleChapterOrdinal` rejects an already-present ordinal.

The core runtime also separates chapter append from `CompleteTaskWork` in
`pkg/jobdb/runtime/core/runtime_task.go`. That is a reason to investigate other
backends, not evidence that every backend exhibits the same failure or lacks
other recovery mechanisms.

## Reproduction and limits of the evidence

A standalone probe used a disposable SQLite database, the real workflow engine,
and a worker that suspends on `input:collect_user_input`. No c2j review feature or
HTTP handler was involved.

After the worker suspended, a second SQLite connection installed this trigger:

```sql
CREATE TRIGGER reject_review_resume
BEFORE UPDATE OF route_task_type ON jobdb_jobs
WHEN OLD.route_task_type = 'input:collect_user_input'
 AND NEW.route_task_type = ''
BEGIN
  SELECT RAISE(ABORT, 'injected failure after outcome persistence');
END;
```

The probe called the waiting task handle's `Finish` with a response and one
artifact, read back the output chapter, removed the trigger, closed the runtime
and database connections, reopened the database, retrieved the waiting task,
and retried `Finish` with the same response.

Observed output:

```text
initial outcome: SUSPENDED
first completion: constraint failed: injected failure after outcome persistence (1811)
durable result after failed completion: ordinal=1 artifacts=1
waiting after reopen: input:collect_user_input
identical completion retry: workflow state conflict: chapter ordinal 1 already exists
```

The session probe was run with:

```sh
go run -tags=untested_go_version /tmp/c2j-review-handoff-probe/main.go
```

That path is a temporary investigation artifact, not a checked-in test. The
sequence above records the reproduction independently of that file's lifetime.

This demonstrates persistence across an orderly close/reopen following an
injected scheduler failure. It does not demonstrate a process-kill failure,
remote-backend behavior, eventual recovery under every maintenance path, or the
outcome of cancellation races. Those require separate investigation.

## Requirements to establish before selecting a fix

- What operation constitutes acceptance: the output append, the scheduler
  mutation, or another durable record?
- After an ambiguous failure, how can a caller distinguish its own accepted
  completion from a different completion of the same task?
- Which existing JobDB recovery paths can reconcile chapter history and scheduler
  state? Are they intended to cover external tasks?
- What happens if cancellation or another completion races with acceptance?
- Must recovery happen without a client retry, and what bounds recovery latency?
- Which guarantees are shared across SQLite and remote backends, and which
  storage operations can actually participate in one transaction?

Task identity must include the specific waiting occurrence. Recovering an old
completion must not finish a later task in the same job. Generic task completion
and application-level submission deduplication should be considered separately;
JobDB need not understand review decisions or review submission schemas.

## Alternatives to explore

| Approach | Potential benefit | Questions and tradeoffs |
|---|---|---|
| Atomic backend commit of visible outcome and scheduler transition | Removes the split-state window where the stores support it | Can chapter metadata and scheduler state share a transaction? How are external artifact blobs staged? Does the remote backend offer an equivalent primitive? |
| Treat the committed chapter as authoritative and reconcile scheduling | Builds on durable workflow history; retries or background recovery can repair scheduling | Requires precise acceptance/cancellation ordering, exact task matching, and a way to distinguish matching retries from competing outcomes. Existing reconciliation may already provide part of this. |
| Record acceptance intent with scheduler state, then materialize the outcome | A durable intent or outbox can drive recovery across separate stores | Introduces intermediate states and recovery work; must prevent execution before the output and artifacts are available. May duplicate existing JobDB machinery. |
| Extend an existing recovery or repair path | Could address the observed gap with a smaller change | First establish whether such a path exists, why normal reopen/retry did not invoke it, and whether its guarantees satisfy external completion. |
| Represent external submissions as durable work processed by a worker | May reuse normal job execution, leases, and replay instead of direct external completion | Requires correlation and winner selection across submission and target jobs. May merely move the same consistency problem. Evaluate as an architectural alternative, not a review-specific workaround. |

These are investigation candidates, not recommendations. In particular, the
observed failure alone does not establish that JobDB needs a new public API,
that c2j needs a separate acceptance store, or that one transaction is feasible
across all backends.

## Follow-up validation

When this issue is resumed, turn the probe into a maintained JobDB regression
test and examine the existing recovery contract before choosing an approach.
Exercise failure before and after each durable boundary, reopen/restart,
identical and competing retries, cancellation, and retry after the job has moved
to another waiting task. Cover SQLite and the supported remote runtime paths.

Success means an unambiguous accepted outcome, recoverable scheduling, and no
accidental completion of another task. The implementation should be selected
from JobDB's storage and recovery design, separately from the native review
feature. Until then, leave this issue deferred and do not claim the untested
crash-recovery guarantees are established.
