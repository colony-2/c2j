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
    // NextRoute, and Execution are typed fields. No CLI JSON parsing is needed.
    fmt.Println(job.JobID, job.Status, job.Execution.Status)
}
```

Imports for this fragment: `context`, `fmt`, `time`,
`github.com/colony-2/c2j/pkg/joblist`, and
`github.com/colony-2/jobdb/pkg/jobdb`.
See [the complete compilable application](../../examples/listjobs/main.go).

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
routes, creation-time bounds, page size/token, and execution compatibility.
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
    // Process this page, including item-level execution diagnostics.
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

With the pinned JobDB backend, page size zero defaults to 100 and an unfiltered
backend page is capped at 200. Negative sizes are invalid. With an execution
filter, a positive page size instead bounds the logical matching result page:
multiple backend pages may be scanned to fill it, including for sizes above
200. Sparse matches may require scanning the entire remainder. There is no
separate scan budget; use context deadlines to limit remote work. The filtered
helper rejects a non-advancing backend cursor.

Pagination does not create a transactional snapshot. Job states and demands
can change between calls. A listing is discovery information, not a lease or
a promise that a job remains runnable.

## Execution information

Use `Job.Execution`; do not interpret `ClientPayload` or merge requirements
yourself. `Execution.Demand.Effective` is c2j's recorded effective snapshot,
including already-published overrides. Image/platform, CPU, memory, and
ephemeral-storage values retain their existing `pkg/execution` types and
units. Schema version and demand revision are on `Demand`; these differ from
the job's client-payload revision.

| View status | Meaning and handling |
| --- | --- |
| `specified` | Resolved demand with at least one requirement; use `Demand.Effective`. |
| `unspecified` | Resolved demand with no requirements; caller defaults may apply. |
| `unresolved` | Recipe requirements are not fully known; not equivalent to an empty resolved demand. Known overrides may still be present. |
| `malformed`, `unsupported` | Unusable data or schema; inspect `Diagnostic`, never silently default. |
| `in_flight` | `ACTIVE` job; requirements are unavailable, not an old snapshot presented as current. |
| `not_waiting` | Terminal/non-waiting job; requirements are unavailable. |

Demand is exposed only for waiting statuses (`READY`, `EXPIRED`, `PENDING_JOBS`,
`AWAITING_FUTURE`, `CRASH_CONCERN`). In-flight lexical requirements may not be
published, so active/terminal views have neither `Demand` nor `Initial`.
`Source` identifies `submission`, `yield`, `absent`, or `unavailable` as
applicable. No recipe is resolved and no history is replayed during listing.

Optional compatibility filtering uses the same existing execution model:

```go
memory := "8Gi"
query.ExecutionFilter = &execution.Filter{
    Allocation: execution.Allocation{
        SchemaVersion: execution.SchemaVersion,
        Resources: execution.Resources{Memory: &memory},
    },
    IncludeUnresolved: false,
}
```

Import `github.com/colony-2/c2j/pkg/execution` for this fragment. At least one
allocation fact is required. `IncludeUnresolved` admits unresolved jobs only
when their known requirements match; it never admits active/terminal jobs.

Without compatibility filtering, malformed/unsupported execution information
is an item diagnostic and does not discard the page. With compatibility
filtering, unusable demand fails the page with `*joblist.ExecutionDemandError`,
which identifies the job. No partial page is returned on error. This matches
the CLI's respective modes.

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
configuration and `errors.As` for `*joblist.ExecutionDemandError`. Other remote
errors are wrapped with operation context; JobDB HTTP failures include status
information. Do not depend on error-message text as a stable machine API.
There are no automatic retries or job-state mutations.

## Compatibility and packaging

The supported primary surface is `Config`, `New`, `Client.List`, `Query`,
`Page`, `Job`, `ErrInvalidInput`, and `ExecutionDemandError`. Public advanced
helpers (`BuildRequest`, `Lister`, `ListExecutionJobs`, `JobFromSummary`,
`ExecutionView`, and status/store helpers) are also supported. Existing
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
