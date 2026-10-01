# Default answers when input is unanswered

Add `if_unanswered` beside `form` on an `input` op:

```yaml
- id: approval
  op: input
  inputs:
    form:
      title: Deployment decision
      fields:
        - id: decision
          type: multiple_choice
          question: Proceed with deployment?
          required: true
          options:
            - {value: approve, label: Approve}
            - {value: defer, label: Defer}
    if_unanswered:
      after: 30m
      fields:
        decision: defer
```

Read the answer normally: `${{ sequence.approval.outputs.fields.decision }}`.
The worker supplies the configured answers if it acquires the still-pending input
once the delay has elapsed. A human can still answer after that time if fallback
has not acquired the input. The first successful completion advances the job;
neither path can overwrite the other answer.

`after` is a positive Go duration, such as `30s`, `30m`, or `2h`. Actual execution
requires an available worker and may happen later. Job/op timeouts remain hard
limits; leave enough budget for fallback and subsequent work.

Use `fields` for multiple questions, or `response` for a single question or a
structured response. Reviews use the same syntax with `form.kind: review`.
Optional returned documents use normal stored artifact references in file-upload
answers or `artifact_refs`; paths and inline attachments are not accepted.

```yaml
op: input
inputs:
  form:
    question: Continue automatically?
    type: boolean
  if_unanswered:
    after: 10m
    response: false
```

Answers and `after` can use ordinary recipe templates. Preparation evaluates and
validates them before publishing the request, then persists the answers and the
absolute eligibility time. Restarting or replaying the job does not reset the
clock or reselect the answers. Required questions, choices, structured schemas,
and stored artifact references must validate.

`form.autofill` answers immediately and cannot be combined with `if_unanswered`.
Field `default` values still supply omitted answers; they do not start a timer.
Without `if_unanswered`, existing input behavior is unchanged.

An automatic result has `receipt.actor.id: input-alternate`,
`receipt.actor.kind: automation`, and a stable submission ID derived from the
request. It represents configured answers, not a human review. Existing field
and response output paths stay the same.

## For library clients and worker authors

`input.Runtime.GetForm` exposes `fallback_at`; `GetDetails().Form.FallbackAt`
exposes the same timestamp. This is an eligibility timestamp, not a reason to
reject an answer. Submit human answers through the existing input APIs.

The c2j CLI registers the internal `input-alternate:complete` handler. Applications
that register input ops themselves should register `input.GetAlternateOp()` along
with `input.GetOp()` and their existing autofill handler. The compiler persists
`next_task_alternate` with the prepared activity outcome and forwards it through
JobDB task options. Completion uses the original logical
`input:collect_user_input` task, preserving workspace state and ordinary artifacts.

The implementation requires JobDB v0.0.23 or later for delayed task-alternate dispatch.
Scheduler adapters must keep the original pending route available for external
completion until an alternate is actually acquired. Tests exercise persistent
SQLite and remote SQLite; an independently implemented scheduler adapter needs the
same conformance behavior.
