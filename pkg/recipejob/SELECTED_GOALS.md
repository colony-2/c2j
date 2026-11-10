# Fresh jobs with an existing primary goal

Set `recipejob.BuildStartJobRequest.GoalID` and an explicit `JobID` to submit fresh
instructions to an existing open primary goal. Omission creates a new goal.
`workflowctl.StartJob` and `starter.StartRecipeJobOptions` carry the same typed
selector. Contradictory nonempty option values fail before submission. Recipe
inputs, parent metadata, dependencies and secondary memberships never select a
primary goal. Restart keeps its existing semantics.

The selected goal must belong to the authenticated tenant and registered stable
cell and captured repository. JobDB owns the atomic eligibility check and
publication. A goal closing before publication rejects the request; closure
after publication preserves its acceptance. No client openness precheck replaces
JobDB's retained receipt reconciliation.

## Retain and replay the operation

For a reusable operation, prepare the complete request once:

```go
start, err := recipejob.BuildStartJob(request) // request.GoalID and request.JobID
// Check err.
job, err := starter.PrepareRecipeJob(ctx, start, starter.StartRecipeJobOptions{}, recipes...)
// Check err.
descriptor, err := starter.NewDescriptor(ctx, job)
// Check err; persist descriptor as JSON durably BEFORE calling Submit.
ref, err := descriptor.Submit(ctx, submitter)
```

Check every error. Retain the descriptor after failures, including transport
failure or `jobdb.ErrSubmissionReceiptUnavailable`. Load the saved descriptor and
call `Submit` again; do not prepare a new request, resolve a changed recipe,
recapture a workspace, regenerate a destination, or change metadata. The
versioned descriptor contains input, policy, metadata, schema, dependencies,
availability, and artifact bytes with SHA-256 hashes. It preserves submitted
secret references and does not resolve secrets. Protect it as submitted user
content. Hash-only schema selectors survive JSON round trips. Selected recipe
requests include their schema with its hash so the protected HTTP route needs no
ordinary schema-miss retry. Existing schema selectors remain intact on replay.

Equivalent currently authorized retries recover the captured receipt even after
closure, rename, archive or soft deletion. Changed semantic fields conflict under
the same destination ID. The descriptor's operation is fresh `submit`; it cannot
be changed into Restart. JobDB retains the backend receipt; toy retains it only
for that engine's lifetime. An unavailable receipt is not permission to republish.

## CLI

```sh
c2j submit "Continue implementing the endpoint" --goal GOAL_ID \
  --job-id DESTINATION_ID --request-file ./submission.json --json
c2j submit --replay ./submission.json --json
```

Use the same `--jobdb` destination on replay if it was explicit originally.
`--request-file` creates a new private file and refuses to overwrite one; it is
written and synced before publication. `--replay` rejects semantic overrides.
For workspace submissions, add `--from-workspace` or `--from-workspace-now` on the
first call. A supported broker prepares the resolved workspace/recipe descriptor
without publishing; the CLI retains that response before the separate selected
publication. Replay sends the saved descriptor without capturing files again.
Broker replay remains bound to its current authenticated parent invocation.

## Authority and supported routes

Remote hosts must install JobDB's trusted
`WithPrimaryGoalSelectionAuthorizer` hook after authenticating the caller. It
checks current goal access and independent selection authority, alongside
`jobs.submit`. Never construct a grant from request JSON or equate a worker lease
with selection authority. c2j's embedded runtime acts as its trusted local
administrator. Its local administration wrapper is never applied to remote
clients.

The current restricted/delegated task grant model has no independent selection
grant. It rejects selected requests with
`jobdb.ErrPrimaryGoalSelectionForbidden` before its submission callback, even
when the caller context has a host hook. Ordinary child submission remains
available. Authorized hosts can provide the trusted submitter to a broker;
production workers keep their existing restricted submitter.

JobDB's remote client uses positive capability discovery and the dedicated
selected operation; unknown support and old servers reject. Brokers use
`/v1/child-jobs/primary-goal` and a separate preparation route. Old brokers cannot
ignore the selector on the ordinary endpoint. Selected clients do not follow
redirects or fall back to ordinary submission. Supported brokers carry shared
typed failures through their JSON error codec.

Successful selected results must contain tenant, job ID, primary goal ID, cell
name, immutable CellID and captured repository. c2j preserves all six in Go,
CLI JSON, broker replies and started-job records. Missing or contradictory
identity is ambiguous; no field is fabricated or fetched after publication.
Ordinary historical references keep their previous minimum validation.

Cortex remains responsible for authorized terminal-source selection, exact
`resume_from` binding, approval and recipe compatibility. This API does not
validate a recovery source. pgjobdb owns production transaction, authority and
retry evidence. Local toy/SQLite and HTTP acceptance does not establish either
external product flow or production readiness.
