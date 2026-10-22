# C2J logging proposal

Status: implemented in C2J; upstream JobDB lifecycle logging remains a follow-up.
Date: 2026-10-22. See the [user guide](GUIDE-Logging.md) and
[implementation/integration guide](GUIDE-Logging-Integration.md) for the shipped
API, behavior, and remaining upstream work. Configuration examples below record
the design that led to the implementation.

Deliberately adopt Go `log/slog` as the logging API, make routine per-op messages DEBUG, and emit execution messages only where work actually runs. At INFO, a worker should report useful lifecycle changes and actionable problems. Reading a job story should normally produce no operational logs. The library choice is evaluated below rather than assumed from existing usage.

## Why the current output is misleading

The main issue is where messages are emitted, as well as their level:

| Current location | Behavior | Proposed change |
| --- | --- | --- |
| [`executeOpBody`](pkg/worker/compiler/compiler.go) | Logs `executing op` and `op executed successfully` at INFO around interpreter evaluation. This code also runs when retrieving recorded results. | Remove these execution claims. Keep neutral interpreter diagnostics at DEBUG only when useful. |
| [`recipeJobWorker.Run`](pkg/worker/compiler/job_worker.go) | Logs `recipe execution completed successfully` when the interpreter returns, including during replay. | Use a DEBUG evaluation summary. Reserve job completion messages for the runtime that records the job outcome. |
| [`opExecutor.do`](pkg/worker/ops/op_executor.go) | Logs every returned error at ERROR, including a stack and error chain. Its work includes workspace preparation, before the op body runs. | Emit task-attempt diagnostics here; preserve the failing phase. Let the outcome owner decide whether the failure needs an ERROR. |
| [`workflow.Context.GetLogger`](pkg/workflow/context.go) | Always returns the global logger. Some compiler and executor code also accesses it directly. | Inherit the supplied execution logger and its fields. |
| [`input/management.go`](pkg/input/management.go) | Logs many HTTP, polling, and SSE implementation steps at INFO. | Move routine details to DEBUG; avoid a message for every poll or streamed item at INFO. |
| [`CLI root`](cmd/c2j/internal/cmd/root.go) | Has no shared log-level or log-format flags. | Configure logging once at CLI startup. |

An op returning a recorded failure also must not produce a fresh operational ERROR just because someone opened its story. Conversely, a real failure to reconstruct that story must remain visible.

## Established approach

Go's standard library already supplies structured logging, text and JSON handlers, severity filtering, and a concurrency-safe `slog.LevelVar` for changing thresholds. Keep `*slog.Logger` as the logging API. [Go slog documentation](https://pkg.go.dev/log/slog)

Replay-aware logging is also established practice: Temporal's workflow logger suppresses replay messages by default to avoid duplicate output. C2J should follow that principle, while retaining explicitly labeled DEBUG diagnostics for reconstruction. [Temporal Go workflow documentation](https://github.com/temporalio/documentation/blob/main/docs/develop/go/workflows/basics.mdx#logging)

C2J's durable JobDB history remains the source of truth for what happened. Diagnostic logs need no persistence protocol, deduplication database, or guarantee of exactly-once delivery.

## Library alternatives and recommendation

There is no single logger used by all modern Go applications. The practical shortlist is standard-library slog, Zap, and Zerolog; logr is also relevant for Kubernetes-oriented integrations. These choices differ in API, performance, and ecosystem fit. All require application-specific decisions about levels, fields, error ownership, and replay.

| Option | Strengths | Tradeoff for C2J |
| --- | --- | --- |
| [`slog`](https://go.dev/blog/slog) | Standard-library structured API; replaceable handlers; shared integration point for libraries. | Sampling and automatic context-field enrichment need a handler or other integration. Its stock handlers are not necessarily the fastest for a particular workload. |
| [`Zap`](https://github.com/uber-go/zap) | Mature structured logger with typed fields, a convenient sugared API, and optimized encoding. Its configuration supports sampling, stack traces, and dynamic levels. [API documentation](https://pkg.go.dev/go.uber.org/zap) | Strongest alternative if we need these operational features together. Switching call sites and public logger options creates migration work; JobDB still needs integration. Zap already appears in C2J's dependencies, so dependency count alone is not a reason to reject it. |
| [`Zerolog`](https://github.com/rs/zerolog) | Efficient JSON logging, fluent typed field builders, context integration, sampling, and human-readable console output. | Worth considering for very high logging throughput or a strong preference for its API. Adopting it requires call-site and integration changes; it does not provide a C2J-specific replay advantage. |
| [`logr`](https://github.com/go-logr/logr) | Small logging abstraction with backend adapters, contextual logger support, and Kubernetes ecosystem fit. | Uses numbered verbosity plus errors instead of the proposed DEBUG/INFO/WARN/ERROR policy. Best justified by ecosystem compatibility, which is not currently our primary requirement. |

Recommendation: choose **slog as C2J's API**, **samber/slog-multi for selective logging**, and standard text/JSON output handlers. Embedded callers can supply their own handler. Existing JobDB integration makes that choice cheaper here, but is supporting evidence rather than the whole rationale.

The API and backend can be chosen separately. A specialized handler can later supply sampling, nicer terminal output, or export to an observability system without rewriting application log calls. Zap also provides a slog adapter in its experimental [`zapslog`](https://pkg.go.dev/go.uber.org/zap/exp/zapslog) package; assess its compatibility before adopting it.

Choose Zap instead if profiling shows meaningful logging overhead or we want its integrated configuration and sampling now. Benchmark our actual records and redaction path before making a performance-driven switch: library benchmark rankings depend on fields, handlers, and versions. There is no measured logging-performance problem in this proposal.

Whichever library we choose, execution-versus-replay correctness comes from placing and labeling the records correctly. A backend change alone cannot determine whether an op actually ran.

## Use existing handlers for selective logging

Adopt [`samber/slog-multi`](https://github.com/samber/slog-multi) for its router, exact attribute predicates, and first-match delivery. It handles derived logger attributes and groups, avoiding another custom handler implementation. We need selective verbosity; hierarchical names and inherited thresholds are optional.

The proposed behavior is a normal baseline level plus explicit selections that enable DEBUG and above. For example, keep INFO globally while debugging only `component=worker`, `purpose=story`, or `job_id=J`. Selection adds detail; it never hides baseline warnings/errors from other jobs. Each record is written once even if several selections match.

Implementation outline:

- With no selections, use the standard handler at the baseline level.
- With selections, use the library's `Router().FirstMatch()` with an `AttrValueIs` route per selection, then a baseline-level predicate as the fallback route.
- Send every route to the same output handler, configured to accept DEBUG. Apply the baseline in the fallback predicate, so an INFO output threshold cannot discard selected DEBUG records. Use one shared output handler to retain synchronized writes.
- Attach selection fields once to the appropriate derived logger. Keep them unique, top-level, and immutable within that scope; groups are for payload fields. Rebind job/purpose metadata at the boundary where its value changes.

The remaining C2J code is flag parsing, logger wiring, and the baseline comparison. Reuse the router's predicates and handler lifecycle instead of implementing attribute collection, grouping, or routing ourselves. Pin a tagged release and verify these integration behaviors with our redaction handler. [Router API](https://pkg.go.dev/github.com/samber/slog-multi#Router)

Attribute selection occurs after record construction, so unrelated DEBUG calls may still construct records while selective debugging is enabled. Do not promise that `Enabled(DEBUG)` can identify a matching record before its attributes exist. Avoid expensive payload construction; no custom filtering optimization is proposed without evidence it is needed.

Do not add `slog-context` initially: C2J already carries an execution logger, and `Logger.With` can carry the required fields. If context propagation later becomes repetitive, use [`veqryn/slog-context`](https://github.com/veqryn/slog-context) for that specific job rather than writing our own context handler. Sampling and terminal styling can likewise be added through existing handlers when needed. Seilog and gokeel are unnecessary for the selected behavior.

## Level policy

| Level | Meaning | Examples |
| --- | --- | --- |
| DEBUG | Routine detail for diagnosing behavior. | Task attempt start/end, recipe evaluation, replay progress, catch selection, normal retry decisions, individual HTTP/SSE processing steps. |
| INFO | A useful operational lifecycle change. | Worker start/stop, job submission accepted, newly persisted terminal job outcome. No per-op progress or polling chatter. |
| WARN | Unexpected degradation that needs attention, but recovery or continued service is possible. | Failed cleanup, repeated infrastructure failures with backoff, an unexpected fallback. |
| ERROR | A current request or execution failed and its responsible boundary is reporting that outcome. | An unhandled terminal job failure, unavailable storage preventing service, story reconstruction failure. |

Expected waits, yields, handled op failures, and intentional inspection stops are control flow. They do not warrant WARN/ERROR. A deadline that actually prevents the requested work from completing is a failure; classify cancellation by its meaning at the owning boundary, not by blindly downgrading every `context.Canceled` or `DeadlineExceeded`.

A historical failed job can have a successfully reconstructed story. Those are separate outcomes.

## Replay and execution rules

1. **The interpreter describes evaluation.** If needed, use messages such as `Evaluating recipe node` and `Recipe evaluation finished` at DEBUG. Returning from `DoTask` does not prove that a task ran now.
2. **The actual task worker describes execution.** A task-attempt start/end pair belongs around the worker callback that really performs work. Recorded-result lookup never enters that callback. Use `Task attempt started` and `Task attempt finished`, with an explicit outcome.
3. **Task preparation is distinct from invoking the op body.** A task attempt may fail while restoring Git state. Report `phase=restore_git_state`, for example; do not claim that the op body executed. A message specifically saying `Op body started` must be emitted immediately before invoking that body, and is optional DEBUG detail.
4. **Replay diagnostics describe replay.** Use `Recorded task result restored` only at a point that knows the result came from history. If that information is unavailable to C2J, keep a neutral evaluation message or omit the message. Do not infer replay from elapsed time, task names, or error text.
5. **Job lifecycle messages require an actual transition.** Interpreter completion is insufficient: persistence can still fail, and the interpreter may be replaying. Use the JobDB runtime's authoritative outcome boundary; if it offers no suitable hook, omit the extra C2J completion log until it does.

`ReadOnlyReplay` can identify story/context inspection, but it cannot solve all replay logging. A resumed worker may replay a completed prefix and then perform new work in the same run. Placing execution logs at the task worker boundary handles this without adding a new replay-state API. Any future per-call replay label must come from JobDB's actual recorded-result decision.

For story and restart-context requests, attach `purpose=story` or `purpose=restart_context` to a request-scoped logger. Ordinary execution uses `purpose=run`; it can still include replay. These fields describe the caller's purpose, not whether an individual task executed.

Successful story/context inspection is quiet at INFO. At DEBUG, one reconstruction summary is usually sufficient. Detailed restored-result events can be added where existing replay observers provide trustworthy information; they are not required for the first implementation.

The story service already checks replay completeness in [`recorder.go`](pkg/story/live/recorder.go) and [`replay_validation.go`](pkg/story/live/replay_validation.go). Preserve those checks. Reaching an expected live-history boundary or the requested context boundary is normal; missing recorded history, nondeterminism, or failure to load required data is not. The compiler's existing [`logReplayCacheMiss`](pkg/worker/compiler/replay_log.go) helper alone cannot make that distinction.

## Configuration and embedding

Proposed global CLI settings:

| Flag | Environment variable | Values | Default |
| --- | --- | --- | --- |
| `--log-level` | `C2J_LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `info` |
| `--log-format` | `C2J_LOG_FORMAT` | `text`, `json` | `text` |
| `--log-debug` (repeatable) | None initially | One exact `field=value` selection per flag | No selections |

Precedence: explicit flag, then environment, then default. Reject invalid values before starting work. These are process settings, not recipe fields or persisted job inputs.

For `--log-debug`, initially support the string fields `component`, `purpose`, `job_id`, `node_path`, `op_type`, and `event`. Split on the first `=`; reject unsupported fields and empty values. Repeated selections are ORed. There is no regex, wildcard, hierarchy, or expression language. Missing fields do not match. Document component names with their call sites; start with `worker`, `compiler`, `story`, `input`, and `git`. Labels on dependency loggers describe the dependency's actual subsystem, not a guessed individual op.

Logs go to stderr. Command results, including existing `--json` output, keep their existing stdout contract. `--log-format=json` formats diagnostic records only. Subprocess output and stored op artifacts are separate from C2J diagnostic logging and must not be silently filtered by `--log-level`.

For example, after implementation:

```sh
# Detailed structured diagnostics for a CLI invocation.
C2J_LOG_LEVEL=debug C2J_LOG_FORMAT=json c2j <command> <arguments>

# Keep only warnings and errors.
c2j --log-level=warn <command> <arguments>

# Keep INFO elsewhere; enable DEBUG for worker records.
c2j --log-level=info --log-debug=component=worker <command> <arguments>

# Debug one job, or all records associated with story reconstruction.
c2j --log-debug=job_id=J <command> <arguments>
c2j --log-debug=purpose=story <command> <arguments>
```

Embedded callers provide a `*slog.Logger` through service/runtime options. Thread it through the compiler, task workers, story service, and JobDB's existing `EngineBuilder.WithLogger`. Fall back to the default logger only when none was supplied. Library calls must not change the process's global level to silence one replay request.

A host that needs runtime baseline changes can retain a `slog.LevelVar`; both the standard handler and the router's baseline predicate should read that variable. Embedded callers can construct the same selective handler through a helper with typed options, or supply their own logger. CLI flags and selections are read at startup. A remote control endpoint, config watcher, hierarchical inheritance, and extra levels such as TRACE are outside the initial scope.

## Record conventions

Use a short factual message and structured fields. Add a stable `event` name for filtering and tests; reserve prose for people. Keep a small set of names near their call sites rather than building an event registry.

| Field | Convention |
| --- | --- |
| `event`, `component` | For example, `task.attempt.finished`, `worker`; `story.reconstruction.finished`, `story`. |
| `tenant_id`, `job_id` | Present whenever known. Preserve existing identifier representations. |
| `node_path`, `op_type`, `task_type`, `step` | Include applicable existing task metadata; avoid competing `op`/`operation` aliases. |
| `invoke_hash`, `attempt`, `chapter_ordinal` | Include only when supplied by the runtime. Do not manufacture missing attempt or chapter identities. |
| `purpose` | `run`, `story`, or `restart_context`, where applicable. |
| `outcome` | The outcome of the event being logged: for example, `succeeded`, `failed`, `canceled`, or `waiting`. |
| `duration_ms` | Elapsed time of the current action, never a replay lookup presented as the original op duration. |
| `error`, `phase` | One consistently named error field; failing preparation/execution phase when known. |

Task success means the task contract completed successfully. An output such as `success=false` must be interpreted according to that op's contract; receiving a result alone is not evidence of business success.

Examples below omit timestamps and some identifiers for readability:

```text
DEBUG msg="Task attempt started" event=task.attempt.started component=worker purpose=run job_id=J node_path=build op_type=command_execution
DEBUG msg="Task attempt finished" event=task.attempt.finished component=worker purpose=run job_id=J node_path=build op_type=command_execution outcome=succeeded duration_ms=812
DEBUG msg="Story reconstruction finished" event=story.reconstruction.finished component=story purpose=story job_id=J outcome=succeeded duration_ms=24
```

The last record can describe a story whose stored job outcome was failed. It reports successful reconstruction, not successful job execution. If displaying historical timing in diagnostics, use an explicit name such as `recorded_duration_ms`.

## Error ownership and redaction

Log an operational failure once at the boundary responsible for its final disposition. Lower layers add context and return errors. They may record DEBUG details, but should not each emit ERROR while unwinding the same failure.

For a live job, that owner is the runtime resolving the job outcome after retry/catch handling. For story reconstruction, it is the hosting request boundary. Library APIs return errors; the host chooses how to report them. The CLI's top-level error rendering counts as reporting: do not also emit the same ERROR from every service layer. Background loops own errors they consume rather than return.

Coordinate this with JobDB's existing worker error logging. Do not promise one ERROR per failure while retaining overlapping C2J and JobDB reporters. A persistence failure is a distinct current failure and should be described as such, without claiming a durable job outcome was recorded.

Use a concise error at the normal reporting level. Reserve detailed error chains for DEBUG and stacks for unexpected internal failures or explicit diagnostics. A stack captured during error reporting identifies the reporting site, not necessarily the origin of the failure.

Preserve [`pkg/secrets/log.go`](pkg/secrets/log.go) masking when introducing configurable handlers. It currently wraps the default logger/writer as secret sessions open. Injected or previously captured loggers require explicit coverage: install a masking handler before deriving child loggers, retain masking of fields added by `With`/`WithGroup`, and test the standard `log` bridge for recursion. Avoid swapping handlers after initialization. Library redaction setup must not overwrite the host's logging configuration.

DEBUG is not permission to dump inputs, outputs, credentials, prompts, or artifact contents. Prefer identifiers, counts, and bounded error details at all levels.

## Implementation sequence and acceptance checks

1. Add the shared CLI flags and a small logger-construction helper using slog-multi routing and standard slog output handlers. Wire the supplied logger through existing options and `Context.GetLogger`; cover masking and JobDB integration.
2. Remove the compiler's misleading execution claims and replayed completion INFO. Add truthful task-attempt DEBUG records at actual worker entry/exit. Keep interpreter diagnostics neutral.
3. Assign error-reporting ownership, including JobDB and CLI boundaries. Make story/context reconstruction quiet by default without suppressing actual reconstruction failures.
4. Audit remaining INFO/WARN sites, especially input HTTP/SSE processing and catch selection, using the same policy. Document the new flags in the CLI guide.

Acceptance tests should exercise behavior rather than snapshot every sentence:

- A normal successful run has no per-op INFO records; DEBUG identifies actual task attempts with job/node fields.
- Repeated story reads of successful and failed jobs emit no fresh execution claims or historical-failure ERRORs.
- A resumed job replays its prefix silently at INFO and emits execution DEBUG records only for work that actually runs.
- A task that fails during Git restoration reports that phase and never claims the op body started.
- Expected waits, handled failures, and intentional context-inspection stops are not ERRORs. Real deadlines and incomplete/nondeterministic replay remain reportable failures.
- One terminal failure produces one owning-boundary report, with lower-level context preserved.
- Flag/environment precedence, invalid values, text/JSON formats, and stdout/stderr separation work as documented.
- Component, purpose, and job selections enable matching DEBUG records, including fields supplied through `With`, while leaving unrelated records at the baseline. Baseline warnings/errors still appear; overlapping selections do not duplicate records. Payload groups do not hide the top-level selection fields, and missing fields do not match.
- Concurrent live execution and story inspection retain their own fields and do not change each other's logging level.
- Supplied loggers, derived loggers, and standard `log` messages retain secret masking. Changing a host-owned `LevelVar` changes filtering safely.

No changes to recipe semantics, stored history, chapter numbering, or restart behavior are needed for this proposal.
