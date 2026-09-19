# Guidance request: Task identifiers containing colons after the client-payload migration

## Request

Please advise on the supported way to represent and route task identifiers that
contain colons in the new JobDB API. Should existing identifiers remain valid,
or should clients migrate to another representation? If migration is required,
what representation and migration approach does JobDB recommend?

We are pausing the affected integration work rather than renaming identifiers
or assuming that the new validator should be relaxed. This request asks for the
intended contract and recommended resolution, not a particular implementation.

## Version and observed behavior

The issue was observed while upgrading from `github.com/colony-2/jobdb v0.0.13`
to `v0.0.18-0.20260919024231-85cc496c0f52`, commit
`85cc496c0f5280210b02821bfb3a6eaa07ed9a76`, containing the client-payload
migration.

Existing task identifiers use an application-defined `operation:step` form.
For example:

| Job type | Task type | Resulting route |
| --- | --- | --- |
| `recipe` | `two-step-op:second` | `recipe:two-step-op:second` |
| `recipe` | `input:collect_user_input` | `recipe:input:collect_user_input` |

On task handoff, the upgraded runtime returns:

```text
failed to reschedule job: invalid next need "recipe:two-step-op:second"
```

The same rejection occurs for `recipe:input:collect_user_input`. The pending
task is not made available, so another worker or a human-input handler cannot
complete it through the normal task-discovery/completion path.

## Apparent contract inconsistency

At the pinned revision:

- `pkg/workflow/worker_runtime_support.go` constructs a capability as
  `jobType + ":" + taskType` and extracts task type by splitting at the first
  colon. That representation appears capable of preserving colons inside the
  task type.
- `pkg/jobdb/client_payload.go`, in `RescheduleTaskWait`, splits at the first
  colon but then rejects any additional colon in the task component.

Consequently, the workflow helper can construct a route that the reschedule
validator rejects. We do not know whether this is an unintended compatibility
regression or an intentional restriction that clients must now adopt.

This is a routing question, separate from client payload ownership or update
semantics. Task coordinates are supplied through the new typed handoff fields;
we are not attempting to restore routing information inside client payload.

## Questions for JobDB

1. What is the supported identifier grammar for job types and task types? Is
   the first colon the only routing separator, or are colons forbidden anywhere
   within a task identifier?
2. If colon-containing task types are supported, should the affected clients
   retain their existing identifiers while JobDB resolves the validation
   inconsistency? Which release or revision should they target?
3. If they are intentionally unsupported, what encoding or naming convention
   should clients use? Is there an existing JobDB helper or typed API that
   avoids clients inventing their own escaping scheme?
4. How should that choice be applied consistently to worker registration,
   task invocation, capability polling, list filters, task discovery, external
   completion, and chapter/replay task identity? Which interfaces expose the
   application identifier versus any encoded routing representation?
5. Should unsupported identifiers be rejected at registration or invocation
   validation, before execution reaches a task handoff?

We understand that the client-payload release requires fresh databases and
matching deployment versions; we are not requesting compatibility with old
stored jobs. The concern is compatibility of application identifiers and all
the producers and consumers that use them in a fresh deployment.

## Reproduction and verification

The existing consuming-repository integration tests reproduce the issue:

```bash
GOTOOLCHAIN=go1.26.1 go test ./pkg/worker/compiler \
  -run '^TestMultiStepWithCapabilityClaim$' -count=1
GOTOOLCHAIN=go1.26.1 go test ./pkg/input \
  -run '^(TestSimpleInput|TestSimpleInputRealEngineWaitRestart|TestHTTPHandlersWithMuxRouter)$' \
  -count=1
```

All four fail on the route rejection described above. Separate toy and SQLite
tests using a colon-free task type pass through explicit yield, external task
completion, and preserved client payload. The consumer builds and passes vet
with Go 1.26.1.

Whatever resolution is recommended, the expected result is a consistent
identifier contract across supported runtimes and transports: a valid task
can be registered, invoked, handed off, discovered, completed, and replayed
without identity loss or ambiguous routing. If client changes are necessary,
please include migration guidance covering those boundaries.
