# Persisted job inspection

## Latest recorded workspace changes

`ChangesAPI` reads the authorized job's latest persisted main workspace using
existing task records and Git bundles. It creates no capture, form attachment,
task, artifact, receipt, or durable Git payload. Existing `API`, snapshot/export,
forms, history and approval semantics are unchanged. A displayed selection does
not certify checks or attribute new content to an earlier approval.

```go
service := &jobinspect.Service{
    Runtime: authenticatedRuntime,
    Tenant: authenticatedTenant,
    Authorize: authorizeJobRead,
}
changes, err := jobinspect.NewChangesService(service, jobinspect.ChangesConfig{})
if err != nil { return err }
page, err := changes.History(ctx, jobinspect.ChangesRequest{
    JobID: jobID, Limit: 50,
})
```

The application owns runtime authentication, tenant selection and the additional
job authorization gate. `Authorize` runs before metadata and protected source
reads, including each foreign producer. Supply this gate when your runtime does
not itself enforce detail/artifact authorization. Requests cannot override the
tenant, repository, artifact keys, task/attempt/workspace, review or context.
`Service` itself also implements `ChangesAPI`; its `ChangesConfig` field supplies
configuration to task helpers. Zero configuration is bounded and **offline**, even
when a recorded repository is a local path. The normal inspection interface's
method set is unchanged. Configure services before concurrent use.

### Identity and selection

The submitted root context identifies an explicit main scope. An implicit root
uses unscoped invocations and the submitted or stored owning repository. Missing
or ambiguous root evidence fails; repository/cell equality alone cannot make a
scoped child main. Execution order and actual attempts determine latest state.
Newer unavailable main state blocks older-success fallback. An external wait's
persisted fixed input is labeled `boundary: before`; an in-process checkout is
never inspected. Recorded committed content excludes uncommitted changes.

`ChangesIdentity` returns protocol version 1, tenant/job, recorded repository,
cell/scope, state occurrence/attempt, incoming/after boundary, active base and
its exact metadata origin, comparison base/origin, head, actual trees, validated
object format, exact `ChangesSource` references with original producers, and
configured repository object hashes. State and byte producers can differ.

An explicit `Base` must be a full hash and wins without fallthrough. Otherwise
recorded output/invocation `ParentHash` precedes resolved-base evidence; native
observations supply endpoints when applicable. Intentional empty base differs
from an unknown hash. There is no upstream, initial-checkout or local HEAD
fallback. An excluded base may itself be a merge. A proven unborn workspace
reports `Unborn: true`, a complete empty history and `diff: unavailable`: existing
records do not encode its object format, so `OpenDiff` cannot invent tree hashes.

History proves the **entire chain before pagination**, excludes base, includes
head and orders every commit oldest first by parents, independent of dates.
Each row includes full commit/tree/parent hashes, author and committer identities
and ISO timestamps, Unicode subject and full-sequence position. Any head/interior
merge yields `history: unsupported`, no rows or cursor, and no single/range
reads. Complete ancestry and base membership are still checked. Independently
readable endpoint trees permit All with `history: unsupported`, `diff: complete`.
Missing closure, invalid objects and exhausted limits never become empty success.
Stored commit headers are validated before formatted metadata is read, including
every interior commit and the excluded comparison base. Missing/malformed tree,
parent, author, committer or date headers return `invalid_objects` before any
page or patch. Extension headers and their continuation lines remain supported.
Tree closure checks compare actual object types with directory, regular-file,
executable and symlink modes, including nested entries and intermediate commits.
Stored tree entries must have canonical modes, valid nonempty names, distinct
names, Git byte ordering and nonzero object hashes. Structurally invalid trees
return `invalid_objects` before pagination or any All/single/range patch.
Gitlinks remain opaque; their target objects need not be retained.

`page.NextCursor` is bound to tenant/job, comparison choice, page size, full
sequence position and selection. Continue using the same request plus this
cursor. A fresh request resolves latest; `Selection` on a later request additionally
requires the same state, endpoints and sources. Changed state is rejected before
protected bytes load; changed prerequisite identity is rejected before returning
rows or patch bytes. An operation using resolved immutable sources can finish
while the job advances. The next operation returns `stale_selection`; discard
the cursor/selection and refresh. Tokens grant no authority and save no session.

### Endpoint patches and ownership

```go
meta, patch, err := changes.OpenDiff(ctx, jobinspect.DiffRequest{
    ChangesRequest: jobinspect.ChangesRequest{
        JobID: jobID, Selection: page.Selection,
    },
    From: firstHash, Through: lastHash,
})
if err != nil { return err }
defer patch.Close()
_, copyErr := io.Copy(destination, patch)
closeErr := patch.Close()
if copyErr != nil { return copyErr }
if closeErr != nil { return closeErr }
_ = meta // actual comparison commit/tree hashes, identity, status and byte count
```

Default/`All` compares selected base to recorded head. `Commit` compares its
predecessor to that member. Inclusive `From`–`Through` compares the first member's
predecessor to the last member; endpoints may cross pages. Selectors are
exclusive; abbreviated hashes, nonmembers and reversed ranges fail. The ordinary
two-tree patch uses binary/full-index output, no renames, `a/` and `b/` prefixes,
no external diff or text conversion. It is a net patch, so a final revert may
produce complete empty output with nonempty history. `meta.FromCommit`,
`ToCommit`, `FromTree` and `ToTree` identify the actual comparison, including an
actual format-specific empty tree when there is no predecessor commit.

The complete patch is spooled within budgets before `OpenDiff` returns. The
returned `io.ReadCloser` owns that private spool; EOF, idempotent `Close`, or
caller-context cancellation removes it. Always defer Close, even when stopping
early, and check copy/close errors. Helpers preserve length and terminal-status
checks, so a short/interrupted response cannot appear as successful EOF.

### Retained sources and deliberate repository access

Readers initialize private bare object storage in SHA-1 or SHA-256 format. They
validate bounded bundle headers, advertised heads, packed expansion sizes/delta
result sizes, object inventory and tree/blob closure before returning complete
results. They use applicable retained bundles, then bounded earlier records in
the same workspace, including cumulative ancestor coverage. Exact fixed incoming
and native retained-restart edges allow separately authorized producer lineage.
Sources are deduplicated, cycles rejected, and every used reference is returned.
When a prerequisite is below an earlier bundle's advertised tip, the reader
proves coverage in separate private object storage. An unrelated nearer bundle
cannot hide an applicable older source or contribute objects to the result.
Probes share download, lineage, storage and operation budgets; rejected probes
are removed. Private temporary storage must support hard links between its own
files; storage accounting conservatively includes those links.
Newer, unrelated, foreign-scope or alternative candidate sources cannot replace
the selected head. Only Git source bytes load; ordinary document artifacts do not.

Offline missing closure reports full hashes. Deliberately configure access to
the **recorded** repository to obtain exact missing prerequisite commits:

```go
config := jobinspect.ChangesConfig{
    ResolveRepository: func(ctx context.Context, recorded string) (jobinspect.RepositoryAccess, error) {
        route, ok := applicationRoutes[recorded]
        if !ok {
            return jobinspect.RepositoryAccess{}, &jobinspect.ChangesError{
                Version: 1, Code: "repository_denied", Detail: "repository is not configured",
            }
        }
        return jobinspect.RepositoryAccess{URL: route}, nil
    },
}
```

The resolver is trusted application policy, never caller-provided URLs. It may
return safe typed `repository_denied` or `repository_authentication` errors.
Git authentication/permission failures are also classified without exposing
stderr. `RepositoryAccess.Environment` permits only explicit credential/transport
variables: `GIT_ASKPASS`, `GIT_SSH_COMMAND`, `GIT_SSH_VARIANT`, `GIT_SSL_CAINFO`,
`GIT_SSL_CERT`, `GIT_SSL_KEY`, `SSH_ASKPASS`, `SSH_AUTH_SOCK`, and HTTP(S)/ALL/NO
proxy variables (upper/lower case). Worker credentials/config confer no access.
Fetches use exact hashes, no candidate branch, tags, submodules or checkout.

### Limits, errors and executable examples

`ChangesConfig` provides `GitPath`, private writable `TempDir`, `Limits`, and the
optional resolver. `DefaultChangesLimits()` returns the following finite budgets.
Zero fields select defaults; negative/contradictory or overflowing budgets fail.
Page size cannot exceed 500; configured token/header bounds cannot exceed 64 MiB.

| Budget | Default |
| --- | --- |
| Cursor/selection and bundle header | 64 KiB each |
| One artifact / total downloaded bytes (artifacts and repository packs) | 256 MiB / 512 MiB |
| Lineage candidates / prerequisite sources / depth | 128 / 32 / 16 |
| History commits / stored objects | 10,000 / 100,000 |
| One inflated object / inflated bytes | 64 MiB / 1 GiB |
| Temporary storage / patch output | 2 GiB / 16 MiB |
| Maximum page size | 500 (request default 100) |
| Operation / Git process | 60 s / 30 s |

`DownloadBytes` charges retained artifact streams and the actual received Git
pack stream for each configured fetch, including failed transfers. All fetches
and prerequisite probes share the operation's remaining allowance. An exhausted
allowance prevents a fetch; an overrun stops and waits for the process group and
returns `ChangesError{Code: "limit", Limit: "download_bytes"}` before returning
history metadata or a patch. Git's pack trace travels through a bounded inherited
pipe and is discarded after counting; it creates no saved payload. A successful
missing-object fetch without observable pack bytes fails with `protocol`.

Retained packs are inspected before Git imports them. Git subprocess output is
bounded; temporary disk is supervised during execution. Git receives isolated
HOME/config/templates, no inherited Git directories/alternates/replacements,
no lazy promisor fetch or implicit transport, and no hooks, checkout, textconv,
LFS or submodule following. Unix processes run in a private process group,
killed and waited on cancellation/time/storage limits. Object reads require Unix
process-group supervision; other platforms return a typed protocol error.
All partial initialization,
source-load, patch and transport failure paths release owned resources.

Use `errors.As(err, &diagnostic)` with `*jobinspect.ChangesError`. Stable codes
distinguish `workspace_unavailable`, `workspace_ambiguous`, `stale_selection`,
`invalid_request`, `invalid_cursor`, `invalid_range`, `unsupported_history`,
`missing_source`, `missing_closure`, `invalid_objects`, `source_mismatch`,
`job_denied`, `repository_denied`, `repository_authentication`, `repository_unavailable`, `limit`,
`git_unavailable`, `temporary_storage`, `backend`, `protocol`, `transport`,
`canceled`, `deadline`, and `lease_lost`. Errors include version, operation,
safe detail, missing hashes and limit name as applicable. Helper/CLI JSON retains
these categories; `errors.Is` preserves cancellation, deadlines and lease loss.
Cancellation or deadlines during helper patch payload, terminal-status or final
EOF reads retain their typed identity, and consumption closes the failed stream.
The same identity is preserved during history JSON decoding, its final EOF,
diagnostic-body reads, tree resolution and prerequisite ancestry validation.

[External examples](changes_example_test.go) compile offline/configured policy,
pagination, refresh, range selection, typed errors and cleanup. The exported
[`changestest`](changestest/fixture.go) fixture builder produces real
`gitcommit.PersistCommit` bundles and native job records. It supports independent
Git oracles, checkout deletion, both formats, binaries, reverts, merges,
advancing bases, writer fallback, unavailable/foreign/unchanged states and
native external waits. `RunRecorded` supports restart tests. `Inventory` drains
chapter windows and every task/attempt/artifact page with content hashes;
`ReadOnlyRuntime` rejects and counts all mutation/execution/secret entry points.
`GitInput` uses the fixture's isolated Git configuration for binary tree/pack
fault injection; injected malformed content must also fail an independent Git oracle.
See its [external consumer test](changestest/consumer_test.go).

CLI (offline; inside tasks uses the authorized helper, including `self`):

```sh
c2j job changes JOB --json --limit 50
c2j job changes JOB --json --limit 50 --cursor TOKEN
c2j job diff JOB --all --output all.patch
c2j job diff JOB --selection TOKEN --commit FULL_HASH --output -
c2j job diff JOB --selection TOKEN --from FIRST --through LAST --output range.patch
```

Both commands accept `--base FULL_HASH`. `--selection` requires matching latest
content. Existing snapshot commands retain historical selectors; these new
commands select latest main only. History metadata is JSON. Diff stdout is raw
patch bytes; `--json` sends metadata/diagnostics to stderr. Output files must not
exist and are removed on incomplete writes. Reader failures exit nonzero;
complete All patches can succeed with a separately reported unsupported history.

## General inspection

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
