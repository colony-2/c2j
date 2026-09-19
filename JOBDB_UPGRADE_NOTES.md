# JobDB client-payload upgrade

## Dependency

This tree pins `github.com/colony-2/jobdb` to
`v0.0.18-0.20260919024231-85cc496c0f52` (commit
`85cc496c0f5280210b02821bfb3a6eaa07ed9a76`). This was the latest `main`
revision checked on 2026-09-19. The latest tag, `v0.0.17`, does not include the
client-payload migration. No local module replacement or JobDB fork is used.

The upstream [migration guide](MIGRATION-CLIENT-PAYLOAD.md) describes the new
contract. The older payload feature-request and requirements documents in this
repository are historical proposals, not the implemented JobDB API.

## Deployment is a fresh start, not an in-place upgrade

The new JobDB release requires matching servers, clients, workers, and native
PostgreSQL bindings, plus fresh format-2 databases and fresh/dedicated artifact
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

## Known task-route incompatibility

The pinned JobDB revision rejects routes containing more than one colon in
`RescheduleTaskWait`. Existing c2j task types include `operation:step`, producing
routes such as `recipe:two-step-op:second`. The capability-handoff integration
test fails with:

```text
failed to reschedule job: invalid next need "recipe:two-step-op:second"
```

Do not roll out this pin for workloads requiring those task handoffs until this
is resolved. Preserving the existing identifiers requires an upstream route
compatibility fix; changing c2j's identifiers requires a deliberate naming and
consumer migration. This upgrade does not silently rename them or disable the
regression test.

## Verification

Verification of this pin: `go build ./...` and `go vet ./...` pass with Go
1.26.1, and the focused client-payload tests pass with the race detector. The
full suite finishes with four failing tests caused by the task-route rejection:
`TestMultiStepWithCapabilityClaim`, `TestSimpleInput`,
`TestSimpleInputRealEngineWaitRestart`, and `TestHTTPHandlersWithMuxRouter`.
This is not a passing full-suite upgrade.

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
from submission metadata and job input. The existing capability test remains
the guard for the task-route incompatibility above.
