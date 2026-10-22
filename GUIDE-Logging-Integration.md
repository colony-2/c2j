# Shared logging implementation and integration guide

C2J uses `*slog.Logger` as its public logging type, `github.com/samber/slog-multi` v1.8.0 for selective routing, and standard text/JSON output handlers. The host owns configuration and output. Libraries receive a logger and derive local fields without changing its levels or installing a process default.

This guide separates the integration available in C2J today from the changes recommended for upstream JobDB and hosts such as Pulse/Cortex. Their source repositories are not modified by this C2J change.

## Configure once in the application

Applications already depending on C2J can use its small, runtime-independent [`pkg/logging`](pkg/logging/logging.go) helper:

```go
level := new(slog.LevelVar) // INFO; the host retains this if runtime changes are needed
root, err := logging.New(os.Stderr, logging.Options{
    Level:  level,
    Format: "json",
    Debug: []logging.Selector{
        {Field: "component", Value: "worker"},
        {Field: "job_id", Value: "job-to-investigate"},
    },
})
if err != nil {
    return err
}
root = secrets.MaskLogger(root) // before deriving fields or handing it to JobDB

// Optional host action, not something a request handler/library should do:
level.Set(slog.LevelWarn)
```

Imports are `log/slog`, `os`, `github.com/colony-2/c2j/pkg/logging`, and `github.com/colony-2/c2j/pkg/secrets`. The helper does not read environment variables or mutate globals. The C2J CLI separately implements the flags/environment contract in the [user guide](GUIDE-Logging.md).

The selections are ORed and remain active when the baseline changes. With the example above, WARN becomes the default elsewhere while selected records remain visible at DEBUG and above. Selections are fixed for the constructed logger; there is no live configuration endpoint or registry.

Pass a neutral root logger into C2J. Host-wide fields such as deployment or process identity are fine; C2J binds `component`, `purpose`, and job identity at its own boundaries. Rebinding an already-bound key with `With` appends another field in slog; it does not replace the old value.

## Connect C2J and JobDB in one process

Use the same protected root in all relevant construction paths:

```go
engine, err := jobworkflow.NewEngineBuilder().
    WithRuntime(runtime).
    WithLogger(root).
    PlusWorkers(workset.JobWorker, workset.TaskWorkers...).
    BuildEngine()
if err != nil {
    return err
}

stories, err := story.New(story.ServiceConfig{
    Engine: engine,
    Logger: root,
    // Supply other application dependencies as usual.
})

deps := ops.NewServiceDepsBuilder().
    WithLogger(root).
    WithWorkflowControl(control).
    Build()
```

These use `github.com/colony-2/jobdb/pkg/workflow` (aliased `jobworkflow`), `github.com/colony-2/c2j/pkg/story`, and `github.com/colony-2/c2j/pkg/ops`. Build the dependencies/workset in the normal order for your application; the fragments show the logger-bearing options.

For an explicit lease run, also populate `GetJobForRunRequest.Logger` or the supplied-lease runner's logger option. A runner obtained directly from the runtime is independent of `EngineBuilder.WithLogger`. C2J's CLI configures both engine and direct-run paths.

Inside recipe evaluation, `workflow.Context.GetLogger()` now honors the scoped logger, then the JobDB execution logger, with the process default as a fallback. Nested scopes retain it. `RecipeJobWorkerOptions.Logger` can override the runtime logger for a request-scoped replay. Actual task workers use `TaskContext.Logger`.

The public story service accepts `ServiceConfig.Logger`. Story reconstruction binds `purpose=story`; context inspection binds `purpose=restart_context`. Those request loggers are supplied to the replay compiler without changing the process logger or the engine's global configuration. The lower-level `InspectRestartContext` API accepts an optional final logger argument.

Input management services receive the logger through `ServiceDepsBuilder.WithLogger`. Their routine request, polling, and SSE steps are DEBUG under `component=input`.

## Reuse the pattern without depending on C2J

Upstream JobDB should continue accepting `*slog.Logger`. It need not import C2J or expose a new logging abstraction. A standalone JobDB server or another host can build the equivalent handler directly using slog-multi:

```go
baseline := new(slog.LevelVar)
sink := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
    Level: slog.LevelDebug, // allow selected DEBUG records through the sink
})
handler := slogmulti.Router().
    Add(sink, slogmulti.AttrValueIs("component", "worker")).
    Add(sink, slogmulti.AttrValueIs("job_id", "job-to-investigate")).
    Add(sink, func(_ context.Context, r slog.Record) bool {
        return r.Level >= baseline.Level()
    }).
    FirstMatch().
    Handler()
root := slog.New(handler)
```

Imports are `context`, `log/slog`, `os`, and `github.com/samber/slog-multi` (aliased `slogmulti`). With no selections, use a standard handler with `Level: baseline` instead. This is the same small configuration recipe used by C2J; there is no need for a new shared module merely to carry a logger interface.

Use first-match routing so overlapping rules do not duplicate records. Share one sink so output remains synchronized. The router handles attributes inherited through `With` and `WithGroup`. Keep selector fields at the top level, and do not put the baseline INFO filter ahead of the router: it would discard DEBUG before selection can see it. [slog-multi router documentation](https://pkg.go.dev/github.com/samber/slog-multi#Router)

## Fields shared across components

| Field | Contract |
| --- | --- |
| `component` | Stable subsystem name, e.g. C2J's `compiler`, `worker`, `story`, `input`; an upstream JobDB logger can use `jobdb`. |
| `purpose` | `run`, `story`, or `restart_context` where known. It describes the request, not whether an individual task was replayed. |
| `tenant_id`, `job_id` | Strings identifying the durable job. Include both when available. |
| `node_path`, `op_type` | C2J recipe occurrence and op type. JobDB must not invent these if its task metadata does not provide them. |
| `task_type`, `invocation_id`, `attempt`, `chapter_ordinal` | Runtime-provided task identity. Preserve actual values and omit unavailable values. |
| `event` | Stable machine-filterable event identifier. Human message wording can evolve. |
| `outcome`, `duration_ms`, `error` | Outcome and elapsed time of the current action, plus its error when present. |

Set a key once per scope. When moving to a different job or purpose, derive from the appropriate unbound parent logger. Additional host fields such as `service=pulse`, `service=cortex`, or a trace ID can coexist with this contract. Log lines should contain identifiers and bounded diagnostics; keep large payloads in their existing artifacts.

C2J's selector parser supports exact string matching on `component`, `purpose`, `job_id`, `node_path`, `op_type`, and `event`. Related applications can expose the same flag semantics. No hierarchy is required. `tenant_id` is still included for correlation even though it is not currently a selectable CLI field.

## Upstream JobDB work

JobDB v0.0.46 already accepts a logger on its engine, execution context, task context, and direct-run requests. C2J now uses these paths. Uniform JobDB fields and durable lifecycle events are follow-up work in JobDB itself:

1. Derive JobDB-owned diagnostic loggers locally with `component=jobdb`, `tenant_id`, and `job_id`. Pass the neutral parent to application workers so they can bind their own component without duplicate keys. Preserve host fields throughout.
2. Keep per-task attempts, retries, waits, and recorded-result lookups at DEBUG. Label a restored result explicitly; an observer may receive historical task-start/end events during replay, so those callbacks alone do not prove execution.
3. Emit terminal lifecycle summaries only after the runtime confirms the durable outcome. Successful outcomes can be INFO; unhandled terminal failure belongs to the owning runtime/host's ERROR report. Do not report interpreter return as a committed outcome.
4. Return errors to synchronous callers and let those callers report once. Background loops report errors they consume. Distinguish a stored job failure from a current storage/lease failure; avoid a second ERROR for the same returned failure.
5. Preserve real replay-integrity errors. Do not globally silence ERROR or downgrade every cancellation/cache miss. The caller decides whether a stop was expected.

C2J does not manufacture a terminal job event where no persistence hook exists. CLI execution continues reporting failed outcomes through its existing result/exit handling. Hosts consuming `JobRunOutcome` must inspect its status and `JobError` as well as the returned Go error; a completed run call can carry a failed job outcome.

## Pulse and Cortex

For an embedded C2J runtime, create one root handler per process and inject it as above. Keep the same job identifiers in application logs, C2J logs, and JobDB logs. If a host already has an observability backend, pass its slog logger; C2J does not require the CLI helper or replace that backend.

For subprocess execution, pass C2J's logging flags or environment variables and collect stderr separately from command results. Configure the host and remote JobDB server independently: these settings are not transmitted through job history or RPC, and they do not become recipe inputs. JSON diagnostic records can coexist with existing plain-text CLI errors/progress on stderr; do not assume every stderr line is JSON.

If request metadata propagation later becomes cumbersome, [`slog-context`](https://github.com/veqryn/slog-context) can add context attributes. Place enrichment before selection so the router can see them. C2J currently uses explicit derived loggers and adds no context middleware dependency.

## Redaction and integration tests

Wrap the root with `secrets.MaskLogger` before deriving child loggers or giving it to C2J/JobDB. The wrapper consults active C2J execution redactors when records are emitted, including fields captured before secret resolution. It does not replace the process default. C2J's execution-secret machinery also protects the standard global logging path; that does not remove the need to protect injected loggers. Host-owned secrets need the host's own redaction policy as well.

Only executable startup should call `slog.SetDefault` if legacy/global call sites need the root. With an explicit output handler, Go bridges standard `log` messages through it. Never wrap the built-in default handler and bridge it back into itself; C2J retains its special writer-based handling for that case.

An integration should verify:

- Selected DEBUG is visible; unrelated DEBUG is hidden; ordinary warnings/errors remain; overlapping selectors emit once.
- Derived fields and payload groups survive routing and masking, including secrets learned after logger creation.
- Concurrent live execution and story inspection keep separate purposes and do not alter each other's logging configuration.
- Successful and failed historical jobs replay without emitting new execution claims or historical-failure ERRORs.
- Actual preparation errors do not claim the op body ran; real reconstruction failures remain returned to the host.
- Text/JSON output stays on the diagnostic stream, separate from command/API results.

The C2J tests cover selection, CLI precedence/streams, masking, task preparation versus invocation, resuming a job with a completed prefix, and replay/context inspection against SQLite and remote JobDB runtimes.
