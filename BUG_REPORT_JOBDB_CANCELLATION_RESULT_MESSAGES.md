# Cancellation result helpers discard persisted completion messages

Owner: JobDB. Type: SDK bug / API consistency.

Verified against `github.com/colony-2/jobdb`
`v0.0.77-0.20261108033303-ea45f87bf010` (commit `ea45f87bf010`).

## Problem

JobDB accepts a cancellation `Reason`, persists it as `CompletionDetail`, and
returns it from `GetJob` and `GetJobRun`, including over HTTP. However, result
helpers discard that explanation and return only the sentinel `ErrJobCancelled`
(`"job cancelled"`). Failure results already provide richer errors.

Callers that consume a result error or `JobRunOutcome.JobError` cannot recover
the operator's feedback without a separate detail read, even when the helper
already has the detail in memory. This matters when cancellation interrupts a
pending review: there may be no final output or completed review response that
could contain the explanation.

## Affected paths

- `pkg/jobdb/job_run_output.go`, `GetJobRunResponse.GetOutput`: the cancelled
  branch returns `ErrJobCancelled` without using `r.Job.CompletionDetail`.
  The failure branch returns `jobFailedErrorFromOutcome(...)`.
- `pkg/workflow/run_job_if_leaseable.go`, `classifyJobRunTerminal`: the cancelled
  branch assigns `ErrJobCancelled` to `JobRunOutcome.JobError`, despite receiving
  a `JobInfo` with `CompletionDetail`. Both already-terminal jobs and jobs that
  become terminal during execution use this classification.

The storage and detailed read APIs are already sufficient. Compact listings
intentionally omit full diagnostic text; this report does not request expanding
list payloads or introducing another stored cancellation-message field.

## Minimal reproduction

This isolates the SDK result conversion without needing a runtime:

```go
reason := "  Use existing artifacts.\n请不要生成 PDF ☃\n"
run := jobdb.GetJobRunResponse{
    Job: jobdb.JobRunSummary{
        Status:           jobdb.JobStatusCancelled,
        CompletionStatus: "cancelled",
        CompletionDetail: reason,
    },
}
_, err := run.GetOutput(nil, "tenant")
// errors.Is(err, jobdb.ErrJobCancelled) == true
// err.Error() == "job cancelled"; the supplied explanation is lost.
```

Runtime reproduction:

1. Submit a job and leave it unfinished, optionally suspended on external input.
2. Call `CancelJob(ctx, CancelJobRequest{JobKey: key, Reason: reason})`.
3. Verify `GetJob(...).CompletionDetail == reason` and
   `GetJobRun(...).Job.CompletionDetail == reason`.
4. Call `GetOutput` on that run, or inspect the workflow runner's terminal
   `JobRunOutcome.JobError`.
5. Observe a bare cancellation error instead of the retained explanation.

## Requested behavior

Use the existing completion detail as the message for cancellation as well as
failure. A cancellation result should carry the exact stored text and remain
distinguishable by outcome/status, without requiring consumers to parse text.

Preserve `errors.Is(err, jobdb.ErrJobCancelled)`. One compatible option is a
typed cancellation error with an exact message/detail accessor and an `Is` or
`Unwrap` implementation retaining the sentinel. Reuse an existing general
terminal-result representation if it can provide the same guarantees. Do not
classify cancellation as an ordinary application failure just to carry a message.

For a nonempty reason, `Error()` should include the reason, and a structured
field/accessor should preserve its exact value, including surrounding whitespace,
newlines, Unicode, and text exceeding preview limits. With no supplied reason,
the structured detail should remain empty; the generic sentinel remains a valid
error description. A prior attempt failure must not override the terminal
cancellation or its reason.

## Regression coverage

- Real cancellation and readback through SQLite and the remote runtime.
- Cancellation before execution and while waiting for external input, without
  final outputs.
- `GetOutput` and workflow terminal-result classification retain the message and
  satisfy `errors.Is(..., ErrJobCancelled)`.
- A retained earlier attempt error does not replace the cancellation message.
- Empty and multiline Unicode reasons remain distinct and faithful.
- Existing failure errors retain their messages, causes, and classification.

## c2j handling

`FEATURE_REQUEST_C2J_CANCELLATION_MESSAGES.md` describes a related c2j projection
bug. c2j can fix its native `recipe.await_result_soft` and job-inspection paths
immediately by reading the already available completion detail. That fix does
not repair these JobDB helpers or other callers relying exclusively on result
errors. Track this SDK correction separately; no cancellation-submission or
automatic-retry changes are requested.
