# Persisted job inspection

`Service{Runtime: runtime, Tenant: tenant}` uses an explicitly selected runtime
and tenant. The caller supplies authentication through that runtime; `Authorize`
is an optional additional job-read gate, called before backend reads.

```go
page, err := service.ListJobLeaseEvents(ctx, jobID, jobinspect.LeaseEventOptions{
    PageSize: 100,
    PageToken: token,
})
```

`ListJobLeaseEvents` returns `jobdb.ListLeaseEventsResponse` unchanged, or the
backend error unchanged. Options also include `EventTypes`, `OccurredAfter`
(inclusive), `OccurredBefore` (exclusive), and `IncludeHidden`. No tenant or job
filter can override the service tenant or selected job. The selected ID must be
nonblank; resolve the CLI alias `self` before calling. Empty/invalid targeting
returns `jobdb.ErrInvalidLeaseEventRequest`. All other validation and visibility
use the backend listing policy. Unknown jobs and jobs without visible retained
history yield a successful empty page, without an existence probe.

The shared `API.Inspect` operation is `Request{Operation: "events", JobID: id,
Events: options}`; the result is in `Response.LeaseEvents`. This also works
through the existing authenticated task inspection helper. There are no workers,
background tasks, automatic pagination, per-event reads, or event reads in
ordinary show/list. Errors are never replaced with an empty page. Typed JobDB
cell errors and lease-permission denial survive the task helper transport.

CLI:

```sh
c2j job events JOB --json --page-size 100 --page-token TOKEN
c2j job events self --event-type lease_acquired --page-size 1
c2j job events JOB --occurred-after 2026-11-08T00:00:00Z --occurred-before 2026-11-09T00:00:00Z
```

Other flags: repeatable `--event-type` (`lease_acquired`, `lease_renewed`),
`--include-hidden`, `--output FILE`, and inspection-standard `--jobdb URI` /
`--embed`. Within a task the helper fixes the connection; `self` selects its
current job. Outside a task `self` is invalid. JSON/output is the raw JobDB page
(`events`, optional `nextPageToken`), preserving string identities, timestamp
precision and server order. Default output shows one page of rows and its next
page token. Pagination is a live traversal; refresh by omitting the token.

History starts only when compatible writers are deployed. Empty history is not
proof that a job never ran. Deployment and collection activation are separate
operator responsibilities.

## Submission families

```go
options := jobinspect.DefaultDAGOptions()
options.MaxNodes = 100
family, err := jobinspect.ReadDAG(ctx, service, jobID, options)
```

`ReadDAG` retains the entry, follows runtime `ParentJobID` to the highest readable
ancestor and discovers descendants breadth first. `options.Subtree` skips ascent.
Errors before retaining the entry return an error. Later failures return a usable
version-1 DAG with `Coverage.Complete` false and explicit branch reasons. A caller
must check coverage; a nil error alone does not mean discovery completed.

`family-node` and `family-children` inspection operations require `FamilyOptions{
Version: 1}`, an explicit `JobID`, and `Limit` in 1–100. Only children accept
`Cursor`. Both authorize before one compact `ListJobs` call with the service's
tenant, hidden records and both stores. They load no execution histories or
per-node detail. Results use `Response.Family`, independently versioned with a
non-null node array. Task helpers validate this envelope and preserve safe typed
errors; unsupported workers must be upgraded alongside the CLI.

The DAG's nodes and typed submission edges are sorted by full ID. Coverage,
observation times, effective limits and actual counts distinguish exhausted
listings from unqueried or failed branches. See the repository's
[inspection guide](../../GUIDE-Job-Inspection.md#see-a-jobs-submission-family)
for command flags, coverage states and output/exit behavior.
