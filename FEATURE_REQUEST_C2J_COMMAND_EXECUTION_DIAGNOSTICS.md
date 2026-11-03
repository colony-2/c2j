# Feature request: portable command execution diagnostics

Status: implemented in this checkout with the owner decisions below; not submitted as an external ticket.
Date: 2026-11-03.
Owner: c2j command_execution runtime and durable diagnostic persistence.
Requester: recipes cell.

## Implemented owner decisions (2026-11-03)

The implementation extends **both command execution and extension operations**.
These decisions supersede the original request preserved below:

- One `_c2j/execution/<node-id>.log` replaces the raw stdout/stderr artifacts.
  Failure details are version 2, under `attrs.command` or `attrs.extension`.
  Existing persisted version-1 artifacts remain readable; new consumers migrate.
- Command stdout/stderr are previews capped at 10 KiB each, including the notice
  and JSON encoding, with explicit truncation flags. Truncation automatically
  retains a log on every outcome; an unavailable log is reported honestly.
- Commands retain failure capture by default and support
  `capture_diagnostics: always`. Extensions automatically retain logs on every
  outcome with no author or schema changes. Their stdout remains a JSON protocol;
  individual structured result fields are never silently truncated.
- The serialized extension result/ref envelope has a 128 KiB budget. Oversize is
  an operation failure with evidence. Logs distinguish process success from
  decoding, schema, publication, or size failures.
- Capture is bounded and drains both pipes after limits; it redacts secrets across
  observations and capture cutoffs. Log truncation does not consume the independent
  extension stdout protocol budget.
- Working-directory reporting checks symlinks and supports extension-source roots.
  Unknown execution-environment and tested-content identity are explicit.

The implemented contract, limits, migration instructions, and examples are in
[Execution diagnostics](GUIDE-Command-Failure-Diagnostics.md).

Implementation: `pkg/ops/process/capture.go`, `pkg/ops/executiondiag/report.go`,
`pkg/worker/commandop`, and `pkg/ops/extensions/execution_op.go`.
Tests cover stream capture, bounded previews, redaction, portable roots, extension
protocol/schema/size failures, and durable rebinding/retry/resume/replay in
`pkg/worker/compiler/command_diagnostics_integration_test.go`.

## Original requested outcome (historical; decisions above take precedence)

A later task should be able to understand a command execution from its durable
artifacts: what was submitted, where it ran relative to task roots, what code was
tested when known, what happened, and what output it produced. It must not need
the original machine's filesystem or have to rerun the command to reconstruct
that information.

Extend native command_execution diagnostics with one readable command log per
operation: relative directory first, then command, live interspersed stdout/stderr,
and final result. Name the log after the descriptive node ID.
Preserve the existing stdout/stderr artifacts and error semantics. Add explicit
opt-in capture for successful executions and continue_on_error, so a required
validation can produce durable evidence regardless of its outcome.

This request **partially fits** the recipes cell: this document and the consuming
artifact conventions belong here; runtime capture, path resolution, redaction,
and persistence belong to c2j. c2ops owns any separate limitation in agent tool
execution or permissions. This request concerns native command_execution; it
does not claim to capture every shell command an agent runs inside another op.

Related: [standard agent inbox proposal](PROPOSAL_STANDARD_AGENT_INBOX.md),
[recipe design feedback](RECIPE_DESIGN_IMPROVEMENTS.md), and
[current native diagnostics contract](../GUIDE-Command-Failure-Diagnostics.md).

## Problem and existing capability

Current native failures expose failure.node.id/op and failure.attrs.command with
version, exit_code, timed_out, and references to _c2j/command/stdout.txt and
stderr.txt. Streams are redacted and durable, preserve bytes including trailing
newlines, and survive nested catches and resume. Diagnostic collection problems
must not replace the original failure.

The documented contract does not supply the submitted command, shell, or working
directory. Two files named stdout.txt and stderr.txt cannot establish which
invocation produced them. A catch around a nested hook may know the failing node
but have no supported way to recover its resolved command inputs.

An absolute producer path such as /worker-a/task-123/git/services/api is not a
usable working directory for a consumer running on worker B. Simply adding that
path would preserve the ambiguity about how to reproduce the execution.

The recipe design feedback also records a case where verification passed but
assessment could not find required embedded c2j validation evidence. This feature
supplies execution evidence; recipes must separately authorize validation, route
blockers, and decide whether the evidence proves the requested outcome.

## Proposed artifact interface

Keep existing failure fields and stream references. Add this optional artifact
entry to the same diagnostics map:

```yaml
failure:
  node:
    id: api_integration_tests
    op: command_execution
  attrs:
    command:
      version: 1
      exit_code: 1
      timed_out: false
      diagnostics:
        _c2j/command/api_integration_tests.log: <durable ArtifactRef>
        _c2j/command/stdout.txt: <existing durable ArtifactRef>
        _c2j/command/stderr.txt: <existing durable ArtifactRef>
```

This is a proposed additive extension. c2j should confirm whether adding the
artifact fits its version-1 compatibility rules; use a negotiated version if
necessary without silently invalidating existing consumers. The report is for
humans and agents, not a second status API. Recipes should use typed runtime
status fields for gating, not parse the log.

Recipes bind the readable log as error/api_integration_tests.log on failure.
The runtime need not know the workflow's error/feedback directory conventions.
Keep legacy raw streams in the diagnostics map for compatibility; agents should
receive the single readable log by default. Test-specific reports may accompany it.

Use the original node ID for the basename, with `.log` appended. Validate and
sanitize it as a single filename component; never permit traversal. For duplicate
IDs across nested recipes, callers qualify the binding with a scoped parent or
hook directory. Distinct attempts retain distinct durable references; consumers
binding more than one attempt use attempt subdirectories. Do not generate filenames
from free-form command arguments or an LLM-written description.

Example api_integration_tests.log:

```text
Working directory: services/api (relative to worktree)
Command: python -m pytest tests/integration -q
Shell: bash
Started: 2026-11-03T14:32:10.000Z

2026-11-03T14:32:10.120Z [stdout] collected 12 items
2026-11-03T14:32:10.350Z [stdout] tests/integration/test_export.py ...F
2026-11-03T14:32:10.351Z [stderr] WARNING: test database connection was reset
2026-11-03T14:32:10.352Z [stdout] FAILED test_export_preserves_filters
2026-11-03T14:32:10.352Z [stdout] Expected 3 orders; received 8.

Result: exited with code 1
Duration: 8.2 seconds
```

For multiline run inputs, replace Command with an indented `Submitted script:`
block. Keep the directory and command first. Optional known environment/candidate
facts follow the command/shell header; omit irrelevant metadata and empty fields.
Use a single final result after output has been drained. If capture was incomplete,
state that limitation next to the result. Empty output and failed collection are
different conditions.

Attribute the original producing operation and attempt through durable provenance,
even when an enclosing recipe catches the failure. Do not assign the log to the
catching node. The node basename is for human navigation; its durable artifact
identity distinguishes executions.

## Interspersed stdout/stderr capture

Capture output from both streams during execution and serialize observations into
one log while retaining the originating stream. Prefix readable output lines with
`[stdout]` or `[stderr]`, preceded by an ISO 8601 UTC timestamp with milliseconds.
Prefer plain text without sequence IDs or JSON. Preserve blank lines with their stream labels and preserve each
stream's own order.

The ordering guarantee is the runtime's observed read order. Separate OS pipes
and producer buffering prevent claiming exact child write order. Do not reconstruct
an interleaving from separately persisted stdout/stderr files. A collector that
first drains one pipe can deadlock or misrepresent output; collection must keep
both streams serviced.

Do not buffer one stream until a newline while emitting the other as though no
bytes were observed. When output arrives as partial lines, render observed segments
with explicit partial/continued markers as needed. Group consecutive same-stream
segments only when that preserves their position relative to the other stream.
Use deterministic incremental decoding for split multibyte text. Render carriage
returns/non-text bytes with an explicit policy rather than silently dropping output.
Retain raw stream artifacts for exact bytes under the existing redaction policy.

Redaction must also work across capture chunk boundaries. It must not leak a secret
because a value was split into two observations. Apply redaction before durable
logging; preserve ordering while doing so. If capture is capped or truncated, put
an explicit notice in the log. Do not change the command outcome merely because
log collection failed, or permit diagnostics to cause unbounded memory growth.

For old executions with only separate streams, an optional readable fallback may
contain labeled stdout and stderr sections and say `captured separately; interleaving
unavailable`. It must not claim live ordering. This is a compatibility fallback,
not satisfaction of the live-capture requirement for new executions.

The runtime writes the command header directly from the submitted input. No extra
echo command, automatic set -x, shell re-evaluation, or executable replay file is
needed. Do not rewrite the command to merge streams with `2>&1`, which would lose
stream identity and may alter its execution behavior.

## Contents to capture

| Information | Requested behavior |
| --- | --- |
| Operation identity | Original node ID and scoped node path when available, with attempt provenance retained |
| Submitted command/script | The actual resolved run input, after secret redaction; explicitly identify it as submitted script |
| Working directory | Relative path plus named task root, according to the rules below |
| Shell | Actual selected shell and relevant invocation options, without relying on a producer-only executable path |
| Execution context | Known runtime/container identity or image digest when available; say unavailable rather than inventing it |
| Result | Distinguish normal exit, launch failure, timeout, cancellation, and pre-execution rejection |
| Exit/timeout | Preserve existing exit_code and timed_out meanings; -1 is not a normal program exit |
| Duration | Observed runtime duration when execution started; distinguish a command that never launched |
| Candidate identity | Known repository revision and whether the runtime can establish actual tested worktree identity |
| Output | Live interspersed text with stdout/stderr labels, plus compatible raw artifacts; empty/partial/truncated capture is explicit |

A revision alone is insufficient if the worktree has uncommitted changes. If
runtime snapshot/content identity is available, record it. Otherwise report
dirty or unknown state and explicitly say that the revision does not identify
all tested content. Do not require c2j to commit changes just to produce this
report. Recipes may retain an accompanying candidate diff or snapshot where
needed to prove validation applicability.

Do not dump environment variables. Capture selected safe execution facts and
apply the runtime's existing secret redaction before persistence. Do not enable
shell tracing or expand shell expressions solely for reporting. A multiline
submitted script is not an execution trace, and the report must not claim which
inner line failed unless that fact was independently captured.

### Timestamp convention and GitHub Actions

Adopt the timestamped-text style used by GitHub Actions. Its
[runner logger](https://github.com/actions/runner/blob/main/src/Runner.Common/Logging.cs)
prefixes messages with UTC timestamps in .NET round-trip format; its
[script handler](https://github.com/actions/runner/blob/main/src/Runner.Worker/Handlers/ScriptHandler.cs)
prints the submitted script and shell before command output.

Use `YYYY-MM-DDTHH:mm:ss.SSSZ` for this command log: UTC with milliseconds.
This borrows the convention, rather than promising byte-for-byte GitHub Actions
compatibility: its fractional precision differs, and our stream labels and
relative-directory header are explicit additions. Group markers, ANSI colors,
and GitHub workflow-command interpretation are not required for a plain-text
artifact. Captured process output remains data.

Record the UTC start time once in the header. Timestamp output when the runtime
observes it, before later formatting or persistence. Lines from the same observed
chunk may share a timestamp; do not invent finer timing. Preserve observed order
even if the wall clock moves backward, and measure final duration with a monotonic
clock. UTC supports cross-task comparison but does not guarantee synchronized
machines or causal order across tasks.

A viewer may show compact elapsed values such as `+00:01.237` relative to the start,
while the saved log retains UTC timestamps. Do not write both forms on every line
or require a second log. An optional viewer is not part of the requested runtime
feature. Legacy separately captured streams have unknown per-line times; never
assign them synthetic capture timestamps.

## Portable working-directory rules

1. Capture the directory actually selected by the runtime, not merely the input
   spelling before defaults or path resolution.
2. Express it as a path relative to a known operation root. Use worktree for
   repository code, inbox/outbox for artifact processing, and workdir for other
   task-local directories. Use `.` for the selected root itself. Prefer the most
   specific named root containing the directory; these roots may be nested.
3. Display, for example, `services/api (relative to worktree)`. The conceptual
   pair is root=worktree, path=services/api. It need not become a new input schema.
4. A receiving task resolves that root using its own operation context. Never
   reuse the producing host's absolute prefix as an instruction for the receiver.
5. Check actual containment, including symlinks. Do not disguise an external
   directory as a portable path containing .. or a symlink escaping the root.
6. When no known root applies, mark the working directory as outside known task
   roots and unavailable for portable replay. Do not fabricate a relative path
   or imply the command can be repeated unchanged.
7. Missing/invalid working directories are launch or validation failures. Record
   the requested root-relative location when known and distinguish it from a
   directory where execution actually started.

Example: producer worktree /worker-a/task-123/git and consumer worktree
/worker-b/task-987/git both map the report's services/api to their respective
repository directories. Relocation does not assert identical tools, permissions,
external services, or repository content; those are separate reproduction facts.

If the submitted script embeds absolute operation paths, render known task-root
prefixes with clearly labeled symbolic roots in the portable report, after
redaction. Label that rendering as normalized display, not a verbatim executable
script. Do not alter the script that actually runs or automatically replay the
rendered text. Preserve source command evidence through supported runtime
provenance as policy permits. Arbitrary external absolute paths cannot be made
portable through blind string replacement; disclose that limitation.

## Success, failure, and continue_on_error

Default native failures should gain the node-named .log with the existing diagnostics.
Propose an input such as `capture_diagnostics: always` for callers that need
validation evidence on all outcomes. The input name is for c2j owner review;
it is not an existing supported field.

In that mode, return an additive output such as `diagnostics`, an artifact map
with the node-named log and existing raw stream references. It must work on success and
continue_on_error without turning a successful op into a failure. Failure catches
still receive the same report through failure.attrs.command.diagnostics. Existing
stdout/stderr/exit_code/success outputs retain their current semantics.

This is needed for hooks whose wrapper exits 0 but reports a semantic check
failure, and for successful validation whose invocation must reach assessment.
The command report records the wrapper's observed execution. It must not infer
whether the surrounding hook or entire recipe passed from that exit status.

When input validation fails before launch, a best-effort report can describe the
rejected invocation, but must not claim it executed or fabricate stream output.
When cancelled or timed out, preserve available partial diagnostics while
retaining native cancellation/timeout behavior. Collection errors belong in the
existing diagnostic-error mechanism; they must not replace the original outcome.
Retries create separate reports with separate provenance. Resume/replay must
reuse persisted evidence without reexecuting completed commands.

## Scope and delivery

Minimum delivery: a durable node-named log on native failures with a portable
directory header, submitted command and shell, live attributed output in observed
capture order, final result, and explicit unknowns. Include opt-in all-outcome capture so callers can retain successful
validation evidence. Optional known environment and candidate details improve
reproduction; missing facts must be disclosed rather than blocking diagnostics.

Do not add a generic replay engine, mandatory set -x, workflow finding routing,
or parsing of test-specific results. No new runtime operation is needed. c2j
should publish updated command_execution and failure-diagnostic documentation
and backward-compatible examples. Recipes will bind the readable log under its descriptive filename and retain
a clearly labeled fallback for older runtimes.

The cross-machine outcome is assessed from persisted reports and rebindings, not
from whether the report can blindly be executed on the receiving machine.

## Acceptance statements for the owning cell

Define implementation test statements before coding in c2j and persist them as
job artifacts there. Exact owning-cell source filenames must be resolved by its
maintainer; the file references below identify the affected contracts in this
repository, not invented c2j implementation paths.

| ID | Expectation | Contract files | Priority | Level / dependencies |
| --- | --- | --- | --- | --- |
| CD-01 | A nested command failure produces a node-named log beginning with relative directory and command, followed by attributed output and result. | GUIDE-Command-Failure-Diagnostics.md | Critical | Integration / c2j command runtime |
| CD-02 | Working-directory references resolve under a different task root without retaining the producing machine's absolute prefix. | GUIDE-Command-Failure-Diagnostics.md | Critical | Integration / two relocated task workspaces |
| CD-03 | External directories and escaping symlinks are reported as nonportable rather than falsely represented within a task root. | GUIDE-Command-Failure-Diagnostics.md | Critical | Integration / filesystem fixtures |
| CD-04 | Timeout, cancellation, launch rejection, and partial collection preserve their original outcomes and available diagnostic evidence. | GUIDE-Command-Failure-Diagnostics.md | Critical | Integration / c2j command runtime |
| CD-05 | Successful and continued executions can retain diagnostic artifacts without changing existing outputs or result semantics. | guides/ops/OP_COMMAND_EXECUTION.md | Critical | Integration / c2j command runtime |
| CD-06 | Diagnostic reports and streams redact configured secrets without enabling global shell tracing or copying complete environments. | GUIDE-Command-Failure-Diagnostics.md | Critical | Integration / secret-redaction fixtures |
| CD-07 | Retry attempts retain distinct evidence, while resume and replay preserve prior evidence without rerunning completed commands. | GUIDE-Command-Failure-Diagnostics.md | Critical | Integration / durable runtime |
| CD-08 | Dirty or unknown worktree state cannot be presented as validation of only the recorded revision. | guides/ops/OP_COMMAND_EXECUTION.md | High | Integration / repository fixtures |
| CD-09 | Rebinding logs preserves stream attribution and observed capture order; older separate streams are explicitly labeled without fabricated interleaving. | GUIDE-Command-Failure-Diagnostics.md | High | Integration / artifact persistence |
| CD-10 | Older consumers retain existing failure fields and stream paths when the execution report is added. | GUIDE-Command-Failure-Diagnostics.md | Critical | Integration / compatibility fixtures |
| CD-11 | Normalized command displays disclose substituted task roots and never modify or automatically replay the executed script. | GUIDE-Command-Failure-Diagnostics.md | High | Integration / relocated task workspaces |
| CD-12 | Concurrent stdout and stderr are captured without deadlock, preserving per-stream order and the collector's observed interleaving. | guides/ops/OP_COMMAND_EXECUTION.md | Critical | Integration / concurrent output fixtures |
| CD-13 | Partial lines, split multibyte characters, and chunk-spanning secrets are handled without lost output, false ordering, or secret disclosure. | GUIDE-Command-Failure-Diagnostics.md | Critical | Integration / stream and redaction fixtures |
| CD-14 | Duplicate or unsafe node IDs cannot overwrite another command log or escape the artifact directory. | GUIDE-Command-Failure-Diagnostics.md | Critical | Integration / artifact naming fixtures |

No external ticket or message was sent by writing this request. The historical request was written before the implementation described above.
