# JobDB: repeated explicit restart reuses an older continuation clock and immediately times out

**Update:** fixed upstream in JobDB commit `510d817719aca7902060c07793883132fff17b48`.
This checkout now pins `v0.0.59-0.20261027194840-510d817719ac`. Verification of the
fix is recorded below; the original diagnosis and evidence follow.

An explicit restart at a previously restarted scope can reuse the earlier
restart's continuation timestamp. A finite timeout on a newly entered child
scope is then calculated from that old timestamp, causing an immediate timeout
even though the new restart should give the work a fresh budget.

The supplied execution history demonstrates the stale deadline and immediate
failure. The initial report identified a likely mechanism in JobDB's continuation
handling; automated verification was subsequently performed against the upstream fix.

## Evidence and affected execution

- Evidence: [j7.json](j7.json), especially chapter ordinals 34–42. These are
  stored chapter ordinals, not JSON array positions or UI node IDs.
- Observed date: 2026-10-27; all times below are UTC.
- Worker versions in activations 35, 37, and 39: JobDB `v0.0.55`, execution
  pattern version `3`, c2j
  `v0.0.105-0.20261027045553-cd329e8381df+dirty`.
- Originating failure: op `squashrebasemerge`, node ID `merge`, recipe path
  `build/develop/root/merge/sequence/merge`.
- All three recorded restart markers target its enclosing sequence:

```text
/scope:"$job"#1/attempt:1/scope:"sequence:build"#1/attempt:1/scope:"sequence:develop"#1/attempt:1/scope:"state_machine:root"#1/attempt:1/scope:"state:merge"#1/attempt:1/scope:"sequence:sequence"#1
```

The user described restarting at chapter 40. In this resulting export, the latest
restart marker is chapter 38 and its target is the sequence above; chapter 40 is
the resulting op timeout. The export does not establish which UI control or
chapter-to-scope mapping selected that boundary.

## Expected behavior

An explicit restart should establish a fresh continuation and timeout budget for
the selected boundary and work entered beneath it. A child op first entered by
the new continuation must not receive a deadline anchored to a previous restart.

This differs from worker recovery or inspection of the same continuation:
those must preserve recorded deadlines and must not extend the budget each time
a worker resumes. JobDB's `applyRestart` comment already states that restart
handling prevents copied deadlines from expiring a deliberately fresh
continuation.

In this case the enclosing sequence and ancestor scopes have no finite timeout,
so no shorter ancestor deadline explains the immediate failure.

## Chapters 34–40

| Chapter | Stored creation time | Record | Relevant contents |
| --- | --- | --- | --- |
| 34 | 05:35:37.629182 | `restart` | First restart visible in this portion of the history. Targets the sequence above. Its descriptor contains reconstruction records for the parent `state:merge` scope and attempt. Source job: `3LGOwMQZfKhwMUiBuVpiNuYup6G`. |
| 35 | 05:35:47.923375382 | `activation` | Worker activation following that restart. |
| 36 | 18:35:10.086788 | `restart` | Another restart at the same sequence. Its descriptor contains an older `continuation` for that boundary with `at: 2026-10-27T05:35:47.84496559Z`. Source job: `3LGTK3NOY7mILaT6qsvrIZlax8r`. |
| 37 | 18:35:17.911458502 | `activation` | Worker activation following the second restart. |
| 38 | 19:02:30.297739 | `restart` | Latest restart at the same sequence, with an empty descriptor. The earlier restart chapters remain in the copied history. Source job: `3LI07lrQXkgPrPtJK9TlsvIuLKy`. |
| 39 | 19:02:38.036330720 | `activation` | Latest worker activation. |
| 40 | 19:02:38.036330387 | `result` | Immediate `Timeout` outcome for the child `op:merge` attempt. Reconstruction metadata contains sequence and op declarations anchored at 05:35:47. |

Chapter 36's embedded continuation contains budgets for the job, outer sequences,
root state machine, and merge state. All those deadlines are unset. The important
stale value is the continuation's **timestamp**, which can anchor subsequently
created finite child deadlines even when the copied ancestor budgets are unlimited.

Chapter 40's `metadata.fields.__execution_prefix_v3.stringValue` is a JSON array
of reconstruction records. Its relevant entries are:

| Scope | `at` | `descriptor.total_timeout` | `total_deadline` |
| --- | --- | --- | --- |
| Enclosing `sequence:sequence` | 05:35:47.844965590 | `0` | unset |
| Child `op:merge` | 05:35:47.844965590 | `3600000000000` ns = 1 hour | 06:35:47.844965590 |

The op attempt has the same 06:35:47 deadline. Its result is recorded at
19:02:38, over twelve hours after that deadline:

```json
{
  "error_kind": "Timeout",
  "error": {
    "scope": "total",
    "after": 0,
    "retryable": false,
    "kind": "scope",
    "component": "runner",
    "code": "timeout_total",
    "message": "scope total timed out after 0s"
  }
}
```

The `after: 0` message does not mean that a zero-second timeout was configured.
The finite op deadline is already expired when the latest activation enters it.

Chapter 41 propagates the failure through the enclosing sequence, merge state,
root state machine, outer sequences, and job scope as `AppError` outcomes. Each
retains the originating `c2j_failure.node`:

```json
{
  "id": "merge",
  "path": "build/develop/root/merge/sequence/merge",
  "type": "op",
  "op": "squashrebasemerge"
}
```

Chapter 42 records the failed job-attempt outcome. The enclosing sequences are
propagating the op's failure; they do not have independent one-hour timeouts.

## Suspected mechanism and ownership

The likely defect belongs to JobDB's restart-prefix construction or continuation
selection. Relevant source locations in JobDB `v0.0.55`:

1. `pkg/workflow/execution_history.go`, history loading: restart descriptors are
   expanded into execution events, while `e.restarts[rec.ID]` retains the latest
   restart record for each logical boundary ID.
2. `pkg/workflow/execution_restart.go:19`, `applyRestart`: if the next event is a
   `continuation` with the same boundary ID, its budgets are reused. The test does
   not distinguish a continuation from an earlier explicit restart at that same
   boundary from the continuation belonging to the current restart.
3. `pkg/workflow/execution_history.go:239`, `recordEvent`: consuming a recorded
   continuation sets `e.anchor = next.record.At`.
4. `pkg/workflow/execution_scope.go:154`, `DoScope`: new scope deadlines are
   calculated from `progressTime()`, which returns that anchor. The one-hour
   child budget therefore becomes 05:35:47 + 1 hour, despite activation at 19:02.

This path is consistent with the supplied records. Confirm the exact causal path
with a repeated-restart regression before choosing whether to fix prefix copying,
restart-generation identification, continuation consumption, or a combination.
Simply resetting clocks on every activation would break ordinary recovery.

The `applyRestart` implementation is unchanged in the locally inspected newer
JobDB commit `5365901bfb7e24a9c1d79a31ba38d9befaccb589`
(`v0.0.58-0.20261027184722-5365901bfb7e`). This is a source comparison, not a
runtime reproduction against that commit.

c2j supplies the implicit one-hour op scope limit in
`pkg/worker/compiler/compiler.go`, `DefaultRecipeExecutor.ExecuteOp`. That
default exposes this failure, but is a separate issue: an explicitly configured
finite timeout must also reset correctly on a fresh restart. Do not fix this by
removing timeout enforcement or adding a second restart-clock implementation in c2j.

## Proposed regression scenario and acceptance criteria

Use the real JobDB execution engine and restart API:

1. Define an unlimited restartable parent sequence-like scope containing a child
   scope with a small, explicit finite total timeout and an observable task body.
2. Run a source job far enough to make the parent a valid restart boundary.
3. Explicitly restart at that parent. Run this first continuation far enough to
   persist its continuation metadata and a task outcome or failure.
4. Advance time beyond its child timeout, then explicitly restart the resulting
   job at the **same boundary ID**. Ensure the copied prefix exercises preservation
   of the earlier continuation, as chapter 36 does here.
5. Verify the new child body executes and can succeed. Its effective timeout
   must be anchored to the new continuation, not the first restart's timestamp.
6. Repeat the restart once more to cover a chain of three restart generations.

Additional assertions and variants:

- Cover both restarting the containing scope and restarting the timed child scope
  directly. Keep unlimited ancestors in the primary regression to isolate the bug.
- Also cover finite ancestor policies, preserving the documented parent/child
  budget relationship for fresh restarts.
- Recover the **same** restarted job after its continuation has been committed:
  the recorded deadline must remain unchanged, not receive another fresh budget.
- A task that exceeds the new finite budget must still time out normally.
- Completed work before the selected restart boundary must remain replayed and
  must not execute again.
- Repeated read-only replay must not refresh clocks, invoke workers, or mutate
  history; recorded outcomes must remain authoritative.
- Exercise toy, SQLite, and remote runtimes, including compact reconstruction
  metadata. Use controlled time/barriers where available instead of hour-long
  sleeps or timing-sensitive short leases. Run the focused cases with `-race`.

## Initial investigation status

Confirmed by parsing the supplied export: three restart markers at the same
boundary, an older continuation embedded in chapter 36, the stale child deadline
in chapter 40, and propagation of that op timeout through the outer scopes.

Confirmed by source inspection: the same-ID continuation reuse and logical-clock
anchor path described above. The initial investigation did not include a fix or
an independent executable reproduction.

## Fix verification (2026-10-27)

JobDB commit `510d817719aca7902060c07793883132fff17b48` changes
`pkg/jobdb/runtime/core/execution_protocol.go`: a fresh restart cuts the copied
history before the selected boundary's superseded continuation, including one
embedded in an earlier restart descriptor. It still validates the target scope
or task declaration. Ordinary recovery of the same continuation keeps its budget.

Validation performed against that version:

- JobDB's `TestExecutionRepeatedRestart*` tests passed with `-race`, covering
  repeated restarts, parent/child/task targets, finite ancestor budgets, recovery,
  timeout enforcement, and admission behavior across the supported test backends.
- `TestExecutionRestartCutsBeforeSupersededContinuation` passed with `-race`,
  including continuation records in compact prefixes, restart descriptors, and
  standalone chapters, plus invalid-target validation.
- A temporary read-only test parsed the actual `j7.json` export and called
  `ResolveExecutionRestart` using chapter 38's target. It returned
  `LastStepToKeep: 35` and an empty reconstruction descriptor. This removes the
  stale continuation in chapter 36 while preserving the earlier history. The
  source history was unchanged. The test passed with `-race`.
- As a negative control, restoring the previous `execution_protocol.go` in the
  disposable test checkout made the same `j7.json` assertion fail:
  `LastStepToKeep` was **39**, retaining the stale continuation.
- c2j validation passed with the updated dependency:
  `go test ./pkg/story/... ./pkg/starter ./pkg/worker/compiler ./pkg/executionruntime ./pkg/template`
  and `go test -race ./pkg/story/... ./pkg/starter ./pkg/executionruntime`.

These checks validate restart-prefix construction on the supplied history and
fresh timer behavior in the upstream runtime regressions. They do not submit a
live restart of the user's job or rewrite its existing recorded timeout result.
