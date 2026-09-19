# JobDB client-payload and typed-route upgrade

## Dependency

This tree pins `github.com/colony-2/jobdb` to
`v0.0.19-0.20260919034646-71b6668a65db` (commit
`71b6668a65dbcc08ae46119412c559f54d8a4b32`). This was the latest `main`
revision checked on 2026-09-19. The latest tag, `v0.0.18`, does not include the
typed-route migration. No local module replacement or JobDB fork is used.

The upstream [client-payload guide](MIGRATION-CLIENT-PAYLOAD.md) and
[typed-route guide](MIGRATION-TYPED-ROUTES.md) describe the new contract.
The older payload feature-request and requirements documents in this
repository are historical proposals, not the implemented JobDB API.

## Deployment is a fresh start, not an in-place upgrade

The new JobDB release requires matching servers, clients, workers, and native
PostgreSQL bindings, plus fresh format-3 databases and fresh/dedicated artifact
storage. Old jobs, archives, schedules, leases, and restart history do not carry
over. Schemas and schedules must be recreated and jobs submitted again.

Stop all old processes before switching storage. Preserve any old data needed
for rollback or inspection; do not point this executable at an existing
database expecting conversion. The embedded database and blob directory are
normally under `$HOME/.c2j/embed/default/`; the application does not automatically
delete them or migrate them. Follow the deployment owner's approved fresh-store
procedure. This code update does not reset any database or artifact storage.

## Application changes

- Compiler context wrappers forward `ClientPayload`, `ClientPayloadRevision`,
  and `Yield`. Timeout scopes reject a yield after their deadline. Validation
  and lease-less recipe-test contexts expose no live client snapshot and reject
  yield instead of pretending to publish an update.
- Client payload is preserved by JobDB when an update is omitted. No shared
  `run_policy`/`task_wait` JSON is copied or merged by c2j. Task completion through
  the existing `Finish` path preserves client state automatically.
- Recipe-job listings and CLI list JSON expose `client_payload` and
  `client_payload_revision` separately from job metadata and typed scheduling
  fields. JSON is passed through as raw JSON, not decoded through floating-point
  numbers or interpreted as recipe input.
- Submission metadata now carries the input hash and submission timestamp for
  listings. Recipe identity and other submission details come from metadata,
  not from client payload. Story requests for raw job data load the original
  job input through the job-run API.
- JobDB supports explicit revision-checked JSON Merge Patch and reset updates.
  Patch null members delete properties; reset preserves null-valued properties.
  Reset with JSON `null` stores null, while reset without a value clears the
  payload. Callers must reconcile conflicts rather than blindly refresh the
  expected revision and retry an old update.
- Explicit workflow `Yield` publishes the update and stops the invocation
  without writing a result chapter. Ordinary chapter/task-result writes do not
  publish client-state updates. The new API does **not** provide the
  non-suspending publication proposed in the earlier requirements document.
- Child and restart jobs start without client payload unless initialized
  explicitly. Task inputs/results and job metadata remain separate concepts.

This upgrade adapts the dependency integration; it does not enable recipe
execution requirements or register the staged execution-allocation CLI flags.

## Typed routes resolve the task-handoff blocker

JobDB now represents routing as `Route{JobType, TaskType}` instead of combining
identifiers into a colon-delimited string. Existing identifiers such as
`two-step-op:second` and `input:collect_user_input` remain unchanged through
handoff, discovery, external completion, and replay. Both job and task types may
contain colons; neither is split, escaped, or normalized.

- Listings expose `next_route` with `jobType` and optional `taskType`, plus
  `task_wait` with `inputOrdinal`, `outputOrdinal`, `inputHash`, and
  `resumeJobType`. These replace `next_need` and the flat `task_wait_*` fields.
- Human-readable `NEXT` and run diagnostics render routes as JSON. `run one`
  reports `status=rescheduled` and a separate `next_route` on handoff.
- `--waiting-for` accepts one JSON route per occurrence, for example
  `--waiting-for '{"jobType":"recipe","taskType":"input:collect_user_input"}'`.
  The old `JOBTYPE:TASKTYPE` syntax is intentionally rejected as ambiguous.
- Repeat `--job-type` for multiple literal types; comma splitting and whitespace
  trimming no longer apply to these identifiers.
- `--on-not-ready fail-on-missing-capability` retains its spelling and now tests
  the typed missing route.
- Lazy pending-task input retrieval uses the supplied output ordinal rather
  than assuming it is one greater than the input ordinal.

The [guidance request](JOBDB_TASK_ROUTE_GUIDANCE_REQUEST.md) is resolved. No
identifier migration or downstream JobDB patch is needed.

## Verification

`go test ./...`, `go build ./...`, and `go vet ./...` pass with Go 1.26.1.
Focused client-payload, typed-route, and run-one tests also pass with the race
detector. Verification uses toy/SQLite runtimes and test fixtures; deployment
against a separately provisioned native PostgreSQL server was not exercised.

The previously failing route regressions are passing:
`TestMultiStepWithCapabilityClaim`, `TestSimpleInput`,
`TestSimpleInputRealEngineWaitRestart`, and `TestHTTPHandlersWithMuxRouter`.

Use Go 1.26.x for this tree. The environment's Go 1.27.1 is excluded by the
current transitive `github.com/cockroachdb/swiss` runtime bindings. Go 1.26.1
matches the module's Go 1.26 requirement without enabling unsafe compatibility
build tags or changing unrelated dependencies.

```bash
GOTOOLCHAIN=go1.26.1 go test ./...
```

Focused regressions exercise real toy/SQLite yields, external task completion,
absence of a result chapter on yield, payload revisions, large JSON numbers,
context forwarding, timeout/validation guards, and separation of client state
from submission metadata and job input. Typed-route regressions also cover
colons in both identifiers, discovery/completion/replay, exact task coordinates,
structured list output, CLI filtering, and missing-route diagnostics.
