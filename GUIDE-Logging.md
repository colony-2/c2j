# Logging and selective diagnostics

C2J uses Go `slog`, with `slog-multi` for selective DEBUG output. Logs go to stderr; command results and their existing JSON format remain on stdout.

## Choose how much to see

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--log-level=debug\|info\|warn\|error` | `C2J_LOG_LEVEL` | `info` |
| `--log-format=text\|json` | `C2J_LOG_FORMAT` | `text` |
| `--log-debug=field=value` (repeatable) | None | No selections |

Explicit flags override environment values. Invalid levels, formats, and selection fields fail before the command starts work. Level names are case-insensitive; formats and selection values are case-sensitive.

```sh
# Normal diagnostics as JSON; job/command output still goes to stdout.
C2J_LOG_FORMAT=json c2j run --job-id "$JOB_ID" 2>diagnostics.jsonl

# Debug worker activity while retaining INFO elsewhere.
c2j --log-debug=component=worker run --job-id "$JOB_ID"

# Debug a particular job.
c2j --log-debug="job_id=$JOB_ID" run --job-id "$JOB_ID"

# Debug either worker activity OR compiler evaluation.
c2j --log-debug=component=worker --log-debug=component=compiler run --job-id "$JOB_ID"

# All diagnostics, or only warnings/errors.
c2j --log-level=debug run --job-id "$JOB_ID"
c2j --log-level=warn run --job-id "$JOB_ID"
```

Supported selection fields are `component`, `purpose`, `job_id`, `node_path`, `op_type`, and `event`. Matching is exact against a top-level string field. Repeated selections are **OR**, and overlapping matches print a record once. A selection enables DEBUG and above for its matching records, while other records use the baseline. For example, a WARN baseline with a worker selection still shows worker INFO and DEBUG.

Selections do not restrict ordinary warnings/errors to the selected job. They are diagnostics controls, not authorization controls. There is no wildcard, regex, hierarchy, or AND-expression syntax. A missing field does not match.

## Useful fields and events

| Field | Values used by C2J |
| --- | --- |
| `component` | `worker` for actual op task processing; `compiler` for recipe evaluation; `story` for story/context APIs; `input` for human-input HTTP/SSE processing. |
| `purpose` | `run`, `story`, `restart_context` at their respective execution/inspection boundaries. |
| `job_id`, `tenant_id` | Durable job identity, when available. |
| `node_path`, `op_type` | Recipe occurrence and registered op type, when known. |
| `event` | Stable identifier on the execution/inspection summaries described below. |

Not every dependency or older diagnostic supplies all fields. Git restoration diagnostics currently belong to `component=worker` and identify the phase with `phase=restore_git_state`. Task start records may precede decoding the node path; finished records include it when decoding succeeded.

Routine per-op processing is DEBUG. INFO is reserved for useful operational lifecycle information. WARN indicates unexpected degradation; ERROR belongs to the boundary reporting a current failed request or execution. Returned errors and CLI exit statuses still report failures when logging is filtered out.

The actual task worker emits `task.attempt.started` and `task.attempt.finished`. The latter includes `outcome` and `duration_ms`. `op.body.started` means the worker reached the actual op invocation; a workspace restoration failure does not emit it. Preparation failures have a DEBUG `op.processing.stopped` record with the current phase and error.

## Reading a job story

Story and restart-context APIs normally produce no INFO output. To diagnose an embedded host's story reconstruction, configure its logger to select `purpose=story`; select `purpose=restart_context` for context inspection. These flags configure the C2J process only; they do not remotely change a Pulse, Cortex, or JobDB server.

At DEBUG, `story.reconstruction.finished` describes reconstruction's outcome. A historically failed job can have a successfully reconstructed story. `restart_context.inspection.finished` similarly describes the inspection request, including an intentional stop at the selected boundary. `recipe.evaluation.finished` describes interpreter evaluation, including replay, rather than newly performed task work.

Resuming a job can both replay old results and execute new tasks. Only tasks that actually run produce worker execution events. `duration_ms` is the elapsed time of the current action; replay reconstruction time is not the original operation duration.

Invalid or incomplete history still returns an error. Libraries return failures to their host, which should report them once. Diagnostic logs are not a replacement for persisted JobDB history or an exactly-once audit stream.

## Output and secrets

`--log-format=json` changes diagnostic records, not command responses. The CLI's final `Error: ...` message and command progress output retain their existing rendering; stderr is therefore not guaranteed to consist exclusively of JSON log records on every command/error path. Child-process stdout/stderr and op artifacts are not filtered by the diagnostic level.

Known execution secrets remain masked at DEBUG, including captured logger fields. DEBUG does not enable raw input/output or artifact dumps. Selective debugging filters records after construction, so it reduces emitted volume but does not eliminate all unrelated DEBUG processing overhead.

For host configuration, logger injection, and the conventions shared with JobDB, Pulse, and Cortex, see the [implementation and integration guide](GUIDE-Logging-Integration.md).
