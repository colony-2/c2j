# Public job listing API

`github.com/colony-2/c2j/pkg/joblist` is the supported read-only Go API for
remote c2j job discovery. It opens an HTTP(S) connection, constructs c2j's
repository-scoped query, and returns typed job and execution views. It does
not initialize a worker, command runner, recipe executor, embedded database,
or local project configuration.

## First page

```go
client, err := joblist.New(joblist.Config{
    JobDBURI: "https://jobs.example.com/team-a",
})
if err != nil {
    return err
}

ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

query := joblist.Query{
    Repository: "github.com/acme/app",
    JobTypes:   []string{"recipe"},
    Statuses: []jobdb.JobStatus{
        jobdb.JobStatusReady,
        jobdb.JobStatusCrashConcern,
    },
    PageSize: 50,
}
page, err := client.List(ctx, query)
if err != nil {
    return err
}
for _, job := range page.Jobs {
    // TenantID, JobID, RepositorySource, Status, AvailableAt, CancelRequested,
    // NextRoute, CompletionStatus, and BlockingWaitSummary are typed fields.
    fmt.Println(job.JobID, job.Status, job.CompletionStatus)
}
```

Imports for this fragment: `context`, `fmt`, `time`,
`github.com/colony-2/c2j/pkg/joblist`, and
`github.com/colony-2/jobdb/pkg/jobdb`.
See [the complete compilable application](../../examples/listjobs/main.go).

`Job.Title` and `Job.Prompt` expose the optional JSON fields `title` and `prompt`
from summary metadata, preserving stored strings verbatim. Empty fields are
omitted. No additional job, history, recipe, artifact, or goal reads are needed.
Clients may choose a display fallback such as nonblank title, then nonblank
prompt, then goal ID; the API does not populate missing titles itself.

## Explicit configuration and selectors

`Config.JobDBURI` has the same meaning as CLI `--jobdb`: the URL's sole path
segment selects the tenant, **not** an API base path. For example,
`https://jobs.example.com/team-a` connects to `https://jobs.example.com` and
queries tenant `team-a`. URL-escaped tenant IDs are decoded as by the CLI.
Missing tenants, extra path segments, credentials in the URL, queries,
fragments, and `embed:///` are rejected. Create separate clients for separate
tenants; each call selects its own repository.

`Query.Repository` accepts explicit repository identities using existing c2j
normalization and exact metadata matching:

- `github.com/acme/app` becomes `https://github.com/acme/app.git`.
- Explicit HTTP(S)/SSH URLs retain their existing spelling, including whether
  `.git` is present. HTTPS and SSH identities are not automatically equated.
- `git@example.com:acme/app.git` becomes `ssh://git@example.com/acme/app.git`.
- An absolute `file:///...` identity can select previously submitted local
  repository jobs without accessing that path.

Configured cell aliases, `--self`, local path discovery, and recipe selectors
are not resolved by this library. Supply the cell's explicit repository
identity. No environment variables, project files, Git commands, or current
directory discovery supplement these inputs. Even unrelated or invalid local
configuration cannot change the connection or repository selected.

## Queries and pagination

Available query fields include job types, statuses, job IDs, typed job/task
routes, creation-time bounds, and page size/token.
Job and task type identifiers are literal, case-sensitive strings; colons have
no separator meaning. Empty `JobTypes` includes all types, not only recipes.
Empty `Statuses` uses the CLI defaults: `READY`, `EXPIRED`, `PENDING_JOBS`,
`AWAITING_FUTURE`, `ACTIVE`, and `CRASH_CONCERN`. Terminal statuses select the
archived store; mixed statuses select both stores.

`List` returns **one logical page**, never an automatic all-results iterator.
`Jobs` is a non-nil empty slice on successful empty results. Advance using
`NextPageToken`; an empty jobs slice alone does not indicate completion:

```go
for page.NextPageToken != "" {
    if page.NextPageToken == query.PageToken {
        return fmt.Errorf("listing cursor did not advance")
    }
    query.PageToken = page.NextPageToken
    page, err = client.List(ctx, query)
    if err != nil {
        return err
    }
    // Render this compact page without fetching details for each row.
}
```

Keep the connection, tenant, repository, filters, and page size unchanged
throughout a traversal. Tokens are backend-owned and opaque: do not decode,
modify, or transfer them to a different query/backend. The current pinned
JobDB implementation uses a cursor without a time-based expiry; this library
adds neither an expiry nor a persistence guarantee across backend upgrades or
replacement. An invalid/rejected token returns an error; restart with an empty
token when appropriate. Consistency between a token and its query is the
caller's responsibility, not a validation guarantee.

With the pinned JobDB backend, page size zero defaults to 100 and a page is
capped at 200. Negative sizes are invalid. Each successful `List` makes exactly
one JobDB `ListJobs` call. It never reads jobs, waits, histories, or artifacts to
enrich the returned page, including when the page is empty with a continuation.

Pagination does not create a transactional snapshot. A listing is discovery
information, not a lease or a promise that a job remains runnable.

## Compact list contract

Lists retain identity, primary goal, parent, schema hash, status, stored route,
completion status/detail, task coordinates, prerequisite IDs, timestamps,
cancellation flag, full client payload and revision, and all application
`Metadata` when supplied by JobDB. c2j also projects familiar labels from metadata.
`BlockingWaitSummary` (`blocking_wait_summary`) contains `waitId` and an optional
`deadline`; it does not explain dependency targets or enumerate goal candidates.

`CompletionStatus` is the persisted outcome, independent of scheduler `Status`.
Unknown values pass through unchanged; an empty status means unavailable, not
success. Query `COMPLETED` explicitly to include completed jobs. A failed attempt
does not establish final failure while a job is retrying.

`BlockingWait` and `OtherGoalIDs` remain unloaded because their construction
requires dependency evaluation or membership reads. Use JobDB `GetJob` for a
selected job or `GetDependencyWait` for one wait. Recipe clients can use
`recipejob.GetRecipeJob`, which reads the selected job's summary and detail.
Do not call these once per row on every refresh.

c2j preserves supplied `CompletionDetail`, full `ClientPayload`, `TaskWait`, and
`WaitFor` without enrichment. The pinned JobDB revision `9c44e3e3bfa1` restores
these fields; remote servers need the corresponding JobDB/pgjobdb update. See
[the restoration migration](../../MIGRATION-C2J-Restored-Job-List-Fields.md).
These are stored fields on rows already read, so their removal was unnecessary
for eliminating extra queries. Workflow summaries also retain supplied run policy
in `ExecutionState`; task data still requires explicit access.

## Execution information

Waiting rows (`READY`, `EXPIRED`, `PENDING_JOBS`, `AWAITING_FUTURE`,
`CRASH_CONCERN`) interpret supplied client payloads locally with the usual
execution-demand semantics. The entire raw payload passes through, including
application data unrelated to execution. Active rows report `in_flight`;
terminal/unknown rows report `not_waiting`, both without demand.

Missing payloads are stored absence: waiting rows use submission requirements
when available, otherwise `Status = "unresolved"` and `Source = "absent"`. An
empty resolved submission reports `unspecified`. Published demand takes priority
over submission requirements. The old `not_loaded` fallback has been removed;
remote backends must implement the restored list contract.

`ExecutionFilter` remains deprecated. A non-nil filter returns
`ErrExecutionDetailsRequired` before any backend call, including when
`IncludeUnresolved` is true. The CLI's `--compatible-with-execution` mode likewise
returns this error. Restored payloads do not define filtered pagination;
re-enabling filters still requires preserving the one-backend-page limit.

`execution.Filter.Match` can evaluate a view from a supplied payload or explicit
inspection; it rejects `not_loaded` views. For selected-job inspection, use
`ExecutionViewFromInfo(summary.Metadata, info)` with a JobDB `GetJob` response.
Schedulers can determine allocation after acquiring a lease using
`execution.Inspect(metadata, lease.ClientPayload())`, without hydrating discovery
rows. The lease remains authoritative if state changes after listing.

See [migration and downstream audit](../../MIGRATION-C2J-Compact-Job-Lists.md).

## Authentication, cancellation, and lifecycle

Supply a caller-owned `*http.Client` via `Config.HTTPClient` for authentication,
custom roots/mTLS, proxies, or timeout policy. Its transport can inject an
authorization header, for example:

```go
type bearerTransport struct {
    base  http.RoundTripper
    token string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    copy := req.Clone(req.Context())
    copy.Header.Set("Authorization", "Bearer " + t.token)
    return t.base.RoundTrip(copy)
}

transport := http.DefaultTransport.(*http.Transport).Clone()
defer transport.CloseIdleConnections()
client, err := joblist.New(joblist.Config{
    JobDBURI: "https://jobs.example.com/team-a",
    HTTPClient: &http.Client{
        Transport: bearerTransport{base: transport, token: token},
        Timeout: 30*time.Second,
        // Do not forward authentication via redirects to another endpoint.
        CheckRedirect: func(*http.Request, []*http.Request) error {
            return http.ErrUseLastResponse
        },
    },
})
```

The fragment assumes a token supplied by your application's secret handling;
the library does not read or mutate process-wide authentication settings.
Without a supplied HTTP client, requests use a 30-second timeout and the
standard default HTTP transport. Standard transport behavior such as proxy
environment handling still applies.

`New` only validates and constructs a connection; connectivity and
authentication failures appear on `List`. A client is reusable concurrently,
starts no background work, and requires no `Close`. Do not mutate a supplied
HTTP client, query, or referenced filter values during calls. The application
owns custom transport resources and may close their idle connections at
shutdown. Returned data is owned by the caller.

Errors preserve `errors.Is(err, context.Canceled)` and
`errors.Is(err, context.DeadlineExceeded)`, including cancellation during HTTP
work. Use `errors.Is(err, joblist.ErrInvalidInput)` for invalid connection/query
configuration and `errors.Is(err, joblist.ErrExecutionDetailsRequired)` for
unsupported list filtering. Other remote
errors are wrapped with operation context; JobDB HTTP failures include status
information. Do not depend on error-message text as a stable machine API.
There are no automatic retries or job-state mutations.

## Compatibility and packaging

The supported primary surface is `Config`, `New`, `Client.List`, `Query`,
`Page`, `Job`, `ErrInvalidInput`, and `ErrExecutionDetailsRequired`. Public advanced
helpers (`BuildRequest`, `Lister`, `ListExecutionJobs`, `JobFromSummary`,
`ExecutionView`, `ExecutionViewFromInfo`, and status/store helpers) are also
supported. Existing
`pkg/recipejob` listing entry points remain available and delegate their shared
execution/status behavior here. They still serve broader recipe workflows;
read-only applications should import `pkg/joblist`.

Use keyed struct literals, tolerate unknown future view statuses
conservatively, and pin a released c2j Go module version. c2j is currently a
v0 module: releases may introduce breaking changes, but any intentional change
to this supported surface or its documented semantics must be called out in
release notes with migration guidance. Additive fields and new statuses may
arrive without a breaking API change. Existing dependency types remain public;
review the pinned JobDB/execution dependency changes when upgrading. No separate
module or new major version is introduced.

Build a standalone consumer with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` or
`GOARCH=arm64`. It needs neither the c2j executable nor Node/npm, a shell, Git,
or a checkout at runtime. Build-time Go/module access is still necessary;
private module access follows your organization's Go setup. HTTPS deployments
need appropriate CA trust and network/DNS configuration even in a minimal
container. No embedded database or execution-toolchain packages are needed on
the listing path; JobDB's own transitive Go dependencies remain.
