# Execution diagnostics

`command_execution` and selector-backed extension operations publish one readable
execution log, `_c2j/execution/<node-id>.log`. The filename is sanitized; durable
artifact references identify the producing job and task attempt. Nested catches
retain the original producer. Retries use distinct references, and resume/replay
reuse persisted evidence without executing the process again.

## Command output limits and capture

The `stdout` and `stderr` command output fields are previews of the beginning of
each stream, redacted before truncation. Each is at most **10 KiB**, including its
notice and JSON string encoding. UTF-8 characters are not split. Small outputs
retain the existing trailing-newline trimming behavior.

Truncation sets `stdout_truncated` or `stderr_truncated` to true and appends:

```text
[Output truncated. See execution log: _c2j/execution/check_tools.log]
```

A truncated preview automatically retains the log, including on success and
`continue_on_error`. If collection fails or a durable reference is unavailable,
the notice says `Execution log unavailable` instead. `error_message` is also
bounded (2 KiB). Diagnostic collection failures do not change the command outcome.

`capture_diagnostics` accepts `failure` (the default) or `always`. `always` also
retains evidence for successful and continued executions. Ordinary small success
or `continue_on_error` results do not create logs in the default mode. Retained
logs appear in the normal artifact map and in the command's `diagnostics` output;
collection problems appear in `diagnostic_errors`.

```yaml
id: validation-evidence
version: '1'
input_schema: {}
sequence:
  - id: check_tools
    op: command_execution
    inputs:
      run: 'go test ./...'
      capture_diagnostics: always
outputs:
  diagnostics: '${{ sequence.check_tools.outputs.diagnostics }}'
```

## Failure contract, version 2

Command failures expose evidence in `failure.attrs.command`; extension failures
use `failure.attrs.extension`. Both have the same structure:

```yaml
failure:
  kind: task_error
  node:
    id: check_tools
    op: command_execution
  attrs:
    command:
      version: 2
      exit_code: 7
      timed_out: false
      diagnostics:
        _c2j/execution/check_tools.log: <stored ArtifactRef>
```

Version 2 replaces `_c2j/command/stdout.txt` and `_c2j/command/stderr.txt`.
New executions no longer publish those files. Existing version-1 artifacts and
references remain readable; consumers of new executions must bind the log path.
Consumers supporting historical runs should branch on `version` and treat old
streams as separately captured, with no reconstructed ordering or timestamps.

`exit_code` describes the process, or is `-1` when no normal exit code is
available (including timeout and launch failure). Timeout and cancellation retain
their native error causes. A process may exit 0 while its extension operation
fails JSON decoding, output schema validation, object publication, or output size
validation. Both results are recorded in the log. Use typed status fields for
control flow, not parsed log text.

Collection problems appear in optional `diagnostic_errors`; they never replace
the original failure. A reference is omitted when attachment or durable identity
is unavailable. Pre-execution rejection inside the operation gets a best-effort
report; failures during preparation or validation before the operation is invoked
may have no execution log.

## Extension authors

No author changes are required. Extensions still receive their existing inputs,
write their existing JSON result to stdout, and write diagnostics to stderr.
The runtime automatically attaches a log on **every outcome**, including success.
No diagnostic fields are injected into the extension's output object or schema.
Successful consumers can use `sequence.<node>.artifacts`; failure consumers use
`failure.attrs.extension.diagnostics`.

The runtime preserves the stdout JSON protocol independently of log capture.
It does not truncate individual result fields or add truncation text to JSON.
The serialized result and artifact-reference envelope has a **128 KiB** budget,
leaving room for runtime metadata within JobDB's default 256 KiB application
payload limit. Oversized results fail with a short diagnostic and a retained log;
use the existing object/artifact channels for large application data. Raw protocol
stdout has a separate 4 MiB capture limit; exceeding it fails explicitly rather
than decoding a partial result. Large stderr output cannot consume that budget.

Errors contain a bounded excerpt and a log reference instead of unlimited raw
stdout/stderr. Extension preparation, manifests, and output schemas are unchanged.
Logs capture the extension subprocess streams, not every inner command an agent
may execute without emitting its output.

## Log contents and limits

The log starts with the selected directory relative to a known root and the
submitted command/script or argv. It includes shell options, node/attempt identity,
extension selector and resolved revision when known, UTC start time, timestamped
stdout/stderr observations, process result, operation result, and elapsed duration.
Environment variables and extension stdin are never dumped. Unknown environment
and tested-content identity are stated explicitly; a base revision alone is not
claimed to identify a dirty worktree.

The directory is captured before execution. Roots are `worktree`, `inbox`,
`outbox`, `workdir`, and (for extensions) `extension_source`. The most specific
containing root wins. Symlink containment is checked. External paths are marked
nonportable. Known root prefixes in commands use explicitly labeled normalized
display; this never changes the command or creates a replayable script.

Ordering is the collector's observed read order, not exact child write order.
Both pipes are drained concurrently. Partial observations use `[partial]` and
`[continued]`; blank lines retain their stream labels. Split UTF-8 characters are
completed incrementally; control/invalid bytes are escaped, including `\r`.
Redaction matches across observation boundaries and masks possible secret prefixes
at capture cutoffs before anything is persisted.

Capture is bounded: 4 MiB of observations, at most 65,536 observations, independent
4 MiB stream buffers, and an 8 MiB rendered log. Pipes continue draining after
limits are reached. Notices identify omitted output; a final result is always
reserved in the rendered log. These are diagnostic text artifacts, not exact-byte
raw stream archives.

## Forwarding failure evidence

```yaml
id: caught-command-diagnostics
version: '1'
input_schema: {}
state:
  initial: check
  states:
    check:
      sequence:
        - id: check_tools
          op: command_execution
          inputs:
            run: 'printf "Required tool unavailable: go\n" >&2; exit 7'
      catch:
        - when: true
          to: inspect
          payload:
            diagnostics: '${{ failure.attrs.command.diagnostics }}'
    inspect:
      op: command_execution
      artifacts:
        check_tools.log: '${{ transition.payload.diagnostics["_c2j/execution/check_tools.log"] }}'
      inputs:
        run: 'cat "${{ context.environment.op.inbox }}/check_tools.log"'
outputs:
  report: '${{ states.inspect.outputs.stdout }}'
```

Bind the same log reference into an agent's inbox for inspection. Keep log contents
in artifacts instead of embedding them in catch payloads or structured outputs.
