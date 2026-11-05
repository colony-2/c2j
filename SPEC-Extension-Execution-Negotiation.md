# Extension execution negotiation

Implemented. Companion [user guide](GUIDE-Extension-Execution-Negotiation.md).

## Purpose

An extension can derive execution requirements from its configuration, inspect
the current allocation, and request another environment before doing work.
c2j preserves the job and retries the same unfinished invocation after handoff.
No new JobDB primitive or recipe schema version is required.

## Process contract

c2j supplies `C2J_EXECUTION_ALLOCATION` to every extension process. Its value is
JSON using the existing allocation schema:

```json
{
  "schema_version": 2,
  "base": "macos-123-456",
  "resources": {"cpu": "4", "memory": "16Gi"},
  "capabilities": ["network"],
  "packages": ["uv:ruff==0.12.0", "nix:nixpkgs#git"]
}
```

Base, resources, and capabilities are worker/provider-reported facts for this
attempt. Unknown facts remain omitted; they are never inferred from requested
requirements or the host. `packages` is the full deduplicated set successfully
prepared by c2j for this process, including repository configuration, recipe and
node scopes, job requirements, and extension dependencies. It is informational,
not a scan of every package on the host. An empty known set is `[]`.
c2j injects the value after inherited, manifest, and invocation environment
settings so they cannot override it. Each resumed process receives fresh facts.
The variable is runtime context, not an input-schema property or part of the
durable task-input hash. Legacy allocation records retain their existing schema;
this protocol's base requests require a worker reporting allocation schema 2.

To request a handoff, the extension must exit **75** and write exactly one JSON
object to stdout:

```json
{
  "execution_required": {
    "base": "macos*",
    "resources": {"memory": "8Gi"},
    "capabilities": ["network"]
  }
}
```

Version one accepts a nonempty requirements object containing `base`,
`resources`, and/or `capabilities`, with their existing c2j validation and units.
It excludes legacy OS/arch/image fields and `packages`. Package information is
read-only; declare package needs through existing recipe/dependency mechanisms. Unknown fields, trailing JSON,
truncated output, and mixed control/result envelopes are invalid. Logs belong on
stderr. This response bypasses the op's successful-output schema and carries no
normal outputs or artifact references.

Both the exit code and valid envelope are required. Exit 0 retains existing
success semantics, including ordinary output properties named
`execution_required`. Other nonzero exits remain failures. An invalid exit-75
response produces a diagnostic failure, never a handoff. Timeout, cancellation,
or lease loss must not be reinterpreted as a request.

## Worker behavior

1. Decode and validate the control response before ordinary error handling,
   output validation, artifact publication, or Git-result acceptance.
2. Combine the request with the current scheduling demand. Capabilities are
   additive, resource minima cannot weaken enclosing requirements, and a base
   selector must refine or encompass the enclosing selector. Incomparable base
   selectors and conflicting job-wide overrides fail instead of publishing a
   demand that cannot satisfy the operation.
3. Compare the requested needs with the current allocation using c2j's existing
   matcher. If **all requested needs are already satisfied**, report a protocol
   error. Do not yield or silently retry.
4. Publish the effective demand through the existing
   `RescheduleExecutionRequest.ClientPayloadUpdate`, then call `TaskContext.Yield`.
   Reuse the payload revision check and preserve unrelated client state.
5. Propagate confirmed suspension through all wrappers. It is not a task failure,
   does not enter recipe `catch`, and does not consume ordinary failure retries.
   Existing total deadlines and cancellation still apply.

The extension must negotiate before side effects. c2j does not accept the attempt
as a successful Git/output checkpoint and cannot roll back external effects.
The launcher and negotiation code must already run on the initial executor;
this protocol cannot negotiate when the extension cannot start.

## Recovery and lifetime

JobDB already supports yielding without recording a completed task outcome.
The subprocess exit is translated by c2j; it does not itself complete the task.
The existing c2j environment guard uses this yield/reschedule mechanism.

There is no separate pending-needs record, checkpoint, or recovery mechanism.
JobDB resumes the unfinished task through ordinary recipe replay. Completed
operations replay from history. The extension runs again with the same logical
inputs and fresh environment information and decides whether it can proceed. An
insufficient executor can receive another request. No request is restored as
recipe state or automatically inherited by later operations or child jobs.
Read-only replay never launches an extension or publishes a handoff. Explicit
restarts follow the existing restart rules.

Only acknowledge a handoff after JobDB confirms the reschedule. If the request
fails or its acknowledgement is lost, stop that execution attempt; do not let
recipe recovery continue under an uncertain lease. Normal lease recovery can
retry the unfinished task. This provides no exactly-once guarantee for process
startup or external effects.

## Implementation and acceptance

The implementation injects allocation information into extension execution and
uses a typed control result that bypasses normal output/artifact/Git acceptance.
The execution runtime reuses `execution.Compare`, client-payload updates, and
`TaskContext.Yield`; `ops.SuspendExecution` remains the successful-result
directive for changing subsequent work.

Tests cover strict control parsing, protected fresh allocation information,
capabilities, encompassing prepared packages, already-satisfied requests,
timeouts, ordinary success/failure semantics, and real extension handoff with
toy, SQLite, and remote runtimes. Handoff tests also cover repeated insufficient
execution, unchanged invocation identity/inputs, catch/retry behavior, supplied
leases, unrelated client state, and read-only replay.
