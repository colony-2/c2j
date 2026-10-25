# c2j goal reliability review

Reviewed against JobDB main commit `86ac742286156ce9f38305b4c8b613df70856095`
(`v0.0.54-0.20261025173555-86ac74228615`). This review covers new jobs under
the agreed goal contract. Old jobs and histories are outside the supported scope.

## Finding and correction

The recipe-specific `RecipeJob` DTO omitted `BlockingWait`, although the general
job DTO, inspection, and story already exposed it. This could conceal structured
goal dependencies from clients using recipe listings. The projection now retains
the full wait, including deadline, dependency state, and recorded winner. A JSON
round-trip regression test checks that these fields survive serialization.

No further functional blocker was identified in the c2j goal execution or
management paths reviewed here. Cortex's goal browser and reverse-waiter UI are
separate delivery work.

## Reliability checks

| Area | Evidence |
| --- | --- |
| Stable child identity | Repeated submission retains tenant, goal, and job identity. Task retries and suspension retain the same goal; new enclosing invocations get new child identities. Replay does not resubmit children. |
| Winner collection | Strict and soft waits collect the replacement candidate's output, with recorded result provenance. Strict collection also checks artifact references. |
| Persistence and recovery | A suspended goal wait survives closing and reopening SQLite, replacing the HTTP endpoint/client, and rebuilding the SDK. The resumed task collects the winner or explicit failure. |
| Replay | Completed waits replay their recorded results without reexecuting the task. Explicit goal failure retains its typed error for strict waits. |
| Restart | Continuation shares the goal; an explicit new goal supplies the new identity to execution and story replay. Concurrent retries with one request ID return the same identity even after the candidate has won. |
| Goal lifecycle | Attachment retries do not duplicate candidates. Failure retries preserve the original failure. Candidate cancellation leaves the goal open. Explicit failure does not cancel candidates, and later success cannot reopen that goal. |
| Closed goals | A successful goal rejects explicit failure. A failed goal rejects new candidates. Attaching a completed successful candidate to an open goal closes it immediately. |
| Competing candidates | Concurrent candidate completions leave one winner; the consumer's recorded binding matches it. Retrying an existing association does not change the winner. |
| Membership and pagination | Candidate lists include primary and additional memberships, hidden and archived jobs. Pagination does not omit or duplicate candidates. Candidate filters do not narrow the goal-wide counts. |
| Discovery | Query-bound cursors reject changed tenants and filters. Resolving one dependency removes only that reverse edge while retaining consumer history and winner bindings. |
| Interruptions | Enclosing task timeout and consumer cancellation remove reverse wait entries without failing the goal. Late producer success does not replace the interrupted wait's recorded outcome. Soft waits do not swallow timeout, cancellation, or storage errors. |
| Management CLI | Real HTTP tests exercise get, candidates, attach, fail, list, waits, and known-wait lookup, including pagination, invalid arguments, retries, and failed consumer history. |
| Tenant isolation | A different tenant cannot read, attach to, or fail the tested goal. |
| Task metadata and permissions | Existing broker, environment, merge, execution-runtime, and permission tests cover paired job/goal metadata, protected current identity, restricted submission, and lease scope. Goal lifecycle management remains a tenant-level API rather than a delegated submit permission. |

The principal integration cases run against both SQLite and the real JobDB HTTP
server backed by SQLite. Mocks are used for targeted error propagation tests,
not as the sole evidence for scheduling, persistence, or management semantics.

Representative test entry points:

- `TestGoalWinnerCollectionRecoveryAndExplicitFailure`
- `TestGoalWaitTimeoutAndConsumerCancellation`
- `TestGoalManagementLifecycle`
- `TestGoalDiscovery` and `TestGoalDiscoveryCommands`
- `TestRestartContinuesGoalAndReconcilesAfterWinning`
- `TestChildSubmissionIdentityAcrossExecutionRetries`
- `TestChildSubmissionIdentityAcrossSuspension`
- `TestSoftGoalCurrentStatusAndResultLookup`
- `TestSoftGoalWaitPropagatesControlAndStorageErrors`
- `TestRecipeJobPreservesStructuredGoalWait`

## Validation results

All checks passed:

- Full repository test suite with cross-package coverage instrumentation for
  recipe operations, recipe job APIs, starter, job context, and schema wrappers.
- Race-detector tests for recipe operations, recipe job APIs, job context,
  schema wrappers, starter, child broker, story, execution runtime, permissions,
  and CLI commands.
- Five consecutive race-detector runs of goal lifecycle, status, interruption,
  discovery, and child-identity tests.
- Five consecutive race-detector runs of continuation/new-goal restart and
  concurrent idempotent restart retries on SQLite and HTTP.
- `go vet ./...` and `git diff --check`.

## Scope of the conclusion

These checks support the c2j implementation's functional completeness for the
agreed scope and give direct regression coverage for the main lifecycle and
recovery risks. Coverage percentages alone would not establish that conclusion.

Database reopening tests exercise persisted recovery after orderly connection
closure; they are not process-kill, disk-corruption, network-partition, or
production-scale load tests. JobDB owns storage atomicity, durable bindings, and
goal state transitions; this review verifies c2j's use of those contracts, not
every backend failure mode. Deploy the matching JobDB contract and retain these
integration tests when updating the pin.

A failed or cancelled candidate can leave an open goal with no runnable
candidates. That is intentional: an operator can attach another attempt or
explicitly fail the goal. Treating the candidate's failure as goal failure would
break continuation semantics.
