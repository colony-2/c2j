# Proposal: `c2j run loop`

## Summary

Add a long-running `c2j run loop` command that monitors one tenant, leases available
jobs from SWF, and executes them with a bounded local concurrency limit.

Recommended command shape:

```bash
c2j run loop --tenant-id <tenant-id> --concurrency 4 --swf-url <http(s)://...>
```

`run loop` is intentionally active terminology: the command does not merely observe
jobs, it claims leaseable work and runs the registered c2j recipe worker. Grouping
it under `run` keeps the job-running command surface together while preserving
`c2j run --job-id ...` for a specific job id.

`c2j run loop` must not support embedded SWF mode. Do not add a `--embed` flag, and
reject `--swf-url embed:///` for this command.

## Goals

- Run available c2j recipe jobs for a specific tenant.
- Bound local parallel execution with an explicit concurrency flag.
- Reuse the existing recipe worker, task workers, op registration, recipe source
  resolution, and SWF lease semantics.
- Keep running until the process is cancelled or the SWF runtime becomes
  unavailable.
- Treat individual job failure as a handled job outcome, not as a fatal worker
  process error.
- Keep `c2j run --job-id ...` as the command for inspecting or continuing one
  known job with live story progress.

## Non-Goals

- Do not add multi-tenant fanout in the first version.
- Do not add embedded runtime support.
- Do not make this command prompt for human input.
- Do not duplicate the recipe execution stack from `runjob`.
- Do not add a second scheduler in c2j. SWF remains responsible for leases,
  availability, dependencies, retries, and durable rescheduling.

## Why Not `--embed`

Embedded SWF mode is useful for short-lived local submit/list/run workflows, but
it does not fit a long-running worker command.

Reasons:

1. The embedded runtime is single-owner today. A long-running worker would hold
   the embedded runtime lock and block other local commands from using the same
   runtime root.
2. `run loop` is intended to behave like a worker process attached to a shared SWF
   runtime. That model only makes sense when submitters and workers can
   coordinate through the same external service.
3. Allowing `--embed` would make local behavior surprising: starting a worker
   could prevent a separate `submit`, `list`, or `run` process from opening the
   embedded runtime.

The command should therefore:

- omit the `--embed` flag entirely
- validate `--swf-url` and fail if the parsed scheme is `embed`
- keep the root help text explicit that `run loop` requires an external SWF runtime

Example validation error:

```text
Error: c2j run loop requires an external SWF runtime; embed:/// is not supported
```

## CLI Shape

Required or defaulted flags:

```bash
c2j run loop \
  --tenant-id <tenant-id> \
  --swf-url <http(s)://...> \
  --concurrency 4
```

Flags:

```text
--tenant-id <id>
  Tenant/project ID to poll. Defaults the same way as submit/list/run:
  explicit flag, then C2J_TENANT_ID, then project self tenant resolution.

--swf-url <url>
  External SWF runtime URL. http:// and https:// are accepted. embed:/// is
  rejected for this command.

--concurrency <n>
  Maximum number of jobs this process may actively run at once. Must be > 0.
  Default: 1.

--await-threshold <duration>
  Optional advanced flag matching existing SWF worker behavior. Long waits past
  this threshold should be recycled/rescheduled rather than sleeping inline.
  Default should match the current SWF/c2j worker default unless there is a good
  reason to expose a different value.
```

Example:

```bash
c2j run loop --tenant-id 123 --swf-url http://127.0.0.1:9047 --concurrency 8
```

## Availability Semantics

`run loop` should not manually decide job availability from list output. It should
use SWF lease APIs through the normal worker loop.

In practice, a job is runnable when SWF can lease it for one of the capabilities
registered by the c2j worker. That means SWF has already considered:

- tenant
- job status
- lease ownership and expired leases
- dependency waits
- future availability
- cancellation state
- required capability / next need

The command can optionally log "no work available" style diagnostics, but it
should not make scheduling decisions based on `ListJobs`.

## Execution Behavior

Startup flow:

1. Complete and validate options.
2. Reject embedded runtime usage.
3. Open the SWF runtime.
4. Register c2j ops.
5. Build the recipe source resolver.
6. Build workflow control and service dependencies.
7. Build the activity registry and recipe workset.
8. Build an SWF engine with:

   ```go
   swf.NewEngineBuilder().
       WithRuntime(runtime).
       WithWorkerTenantId(opts.TenantID).
       WithMaxActive(opts.Concurrency).
       WithAwaitRecycleThreshold(opts.AwaitThreshold).
       PlusWorkers(workset.JobWorker, taskWorkers...).
       BuildEngine()
   ```

9. Run `engine.Run(ctx)` until context cancellation or fatal runtime error.

The existing SWF worker loop already polls a single tenant and limits active
work through `WithMaxActive`, so the first implementation should lean on that
instead of writing a separate c2j scheduler.

## Human Input

`run loop` should not prompt.

If a job reaches a human-input wait, the worker should let SWF suspend or
reschedule the job according to existing recipe/input behavior. Operators can
then use `c2j run`, an input-management command, or an external ops surface to
answer the pending input.

This keeps long-running workers suitable for non-interactive environments.

## Logging and Output

`run loop` should use concise operational logs rather than the single-job story
progress renderer used by `run`.

Suggested startup output:

```text
working tenant=123 swf_url=http://127.0.0.1:9047 concurrency=4
```

Suggested job lifecycle events:

```text
job started tenant=123 job_id=job-... capability=recipe
job completed tenant=123 job_id=job-...
job failed tenant=123 job_id=job-... error="..."
```

If structured logging exists or is added later, this command should use it. The
human-readable output should remain stable enough for operators but not become a
public machine contract in v1.

## Exit Behavior

Expected exit semantics:

- `0`: context cancelled cleanly, for example SIGINT/SIGTERM after active work
  has stopped or drained
- `1`: setup failure, runtime failure, or unexpected worker-loop failure
- `2`: invalid options

Individual job failures should not terminate the worker process. They should be
recorded in SWF as job outcomes and logged by the worker.

## Package Layout

Add:

```text
cmd/c2j/internal/workjob/options.go
cmd/c2j/internal/workjob/service.go
cmd/c2j/internal/workjob/options_test.go
cmd/c2j/internal/workjob/service_integration_test.go
cmd/c2j/internal/cmd/run_loop.go
```

Wire the command from:

```text
cmd/c2j/internal/cmd/root.go
```

Suggested options type:

```go
type Options struct {
    TenantID       string
    SWFURL         string
    Concurrency    int
    AwaitThreshold time.Duration
    WorkingDir     string
    Stdout         io.Writer
    Stderr         io.Writer
}
```

## Shared Worker Construction

`runjob` already builds nearly all dependencies required for recipe execution:

- `c2jops.Register()`
- `jobutil.BuildRecipeSourceResolver()`
- `workerworkflow.SWFWorkflowControl`
- `workerops.NewActivityRegistry()`
- `colonycel.NewBuilder(...)`
- `compiler.NewRecipeWorkerWithOptions(...)`

The implementation should extract that setup into a small internal helper or
keep a deliberately similar helper in `workjob`. Avoid copying large execution
logic or introducing another recipe runner.

One reasonable refactor:

```text
cmd/c2j/internal/jobrunner
```

Responsibilities:

- open/build shared recipe worker dependencies
- return `swf.WorkflowRuntime`, `swf.SWFEngine`, recipe workset/task workers,
  CEL provider, root source resolver, input runtime if needed, and cleanup

`runjob` can keep its single-job story-progress behavior. `workjob` can use the
same registered workers through SWF's worker loop.

## Runtime Opening

Today `swfruntime.OpenWorker(ctx, swfURL, tenantID)` configures worker tenant
identity. `run loop` also needs to set max active work. Add an option-based opener
or build the SWF engine inside `workjob` after opening the runtime.

For example:

```go
type WorkerOpenOptions struct {
    WorkerTenantID string
    MaxActive      int
    AwaitThreshold time.Duration
}
```

The validation for `run loop` should happen before this point so embedded runtime
usage fails with a command-specific message.

## Testing

Minimum tests:

1. Options defaulting resolves `TenantID`, `SWFURL`, stdio, and working dir like
   the other commands.
2. Validation rejects missing tenant ID.
3. Validation rejects missing SWF URL.
4. Validation rejects `--concurrency <= 0`.
5. Validation rejects `embed:///`.
6. Cobra wiring does not define a `--embed` flag for `run loop`.
7. Integration test submits multiple jobs and verifies `run loop` completes them.
8. Concurrency test verifies no more than `N` jobs are active at once.
9. Job failure test verifies one failed job does not stop the worker from
   processing later available jobs.
10. Cancellation test verifies the worker exits cleanly when the context is
    cancelled.

## Documentation

Update `README.md` with a new "Worker Modes" section:

```bash
c2j run loop --tenant-id <tenant-id> --swf-url http://127.0.0.1:9047 --concurrency 4
```

Document clearly:

- `run loop` requires an external SWF runtime
- `--embed` is not available
- `embed:///` is rejected
- `--concurrency` limits active local jobs
- `run` remains the command for running or inspecting one known job

## Open Questions

1. Should v1 include cell filtering, or should it process all c2j recipe jobs in
   the tenant?

   Tenant-only is simpler and matches the current ask. Cell filtering could be
   added later using the same metadata fields that `list` already uses.

2. Should `--concurrency` default to `1` or SWF's current engine default?

   Defaulting to `1` is conservative for a CLI worker and makes capacity
   explicit. Operators can raise it when they are ready.

3. Should job lifecycle output be plain text only in v1, or should the command
   add a `--json`/structured-log mode immediately?

   Plain text is enough for a first implementation. Structured output can be
   added when there is a concrete consumer.
