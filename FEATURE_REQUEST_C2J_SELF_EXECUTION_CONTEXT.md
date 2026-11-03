# Proposal: `c2j self` as agent execution-context discovery

Status: implemented (schema version 1).
Date: 2026-11-03.

## Outcome

An agent should be able to run `c2j self` to answer:

- Which job, task, attempt, and recipe invocation am I executing?
- Which cell and workspace am I operating in?
- What is the state and lifetime of the execution authority supporting this task?
- Which c2j actions can I perform, through which CLI/helper route, and with what restrictions?
- If an action is unavailable, is that a permission denial, a missing capability,
  an expired execution scope, or something the runtime cannot currently establish?

Keep the existing command and `--json` mode. Add execution information to the
ordinary output so agents do not need to discover another command or flag.
No extension-author changes should be required.

Agent instruction:

> Run `c2j self` at the start of a task to discover your current job/task identity,
> workspace, lease status, and available c2j actions. Use `c2j self --json` for
> structured details. Follow the reported routes and restrictions. Recheck after
> a lease or permission failure. Unknown means unverified, not allowed or denied.

The report describes runtime authority and capabilities. It does not supersede
the user's task instructions or grant permission to perform unrelated actions.

## Implementation and sources

The original `self` resolved cell configuration and printed `short_name`, `repo`, `ref`,
`root_repo`, `root_ref`, `pattern`, `packages`, `config_path`, and `jobdb`.
The implementation preserves these keys and adds runtime inspection.

Relevant current sources:

| Source | Information already available |
| --- | --- |
| `cmd/c2j/internal/cmd/self.go`, `cmd/c2j/internal/configinspect/service.go` | CLI and existing cell report |
| `pkg/jobcontext/context.go` | Protected current-job environment, tenant/goal/job identity, operation type, invocation path/sequence/hash, broker connection |
| `pkg/worker/ops/op_executor.go` | Active workspace, operation paths, broker lifecycle, current operation context |
| JobDB `workflow.TaskContext` | `InvocationID`, one-based `AttemptNumber`, and `Step` result ordinal |
| `pkg/permissions/access.go`, `pkg/permissions/policy.go` | Effective operation grant and live deadline/cancellation scope |
| `pkg/executionruntime/permissions.go` | Delegation, controller-mediated submission, child policy restrictions |
| `pkg/executionruntime/runtime.go`, JobDB lease interfaces | Lease identity, execution route, renewal expiry where supported, delegation metadata |
| `pkg/childbroker/broker.go`, `pkg/childbroker/workspace.go` | Authenticated invocation-local helper transport and existing read-only workspace endpoint |

The recipe permission surface currently exposes `jobs.submit` and named
`secrets.resolve`. Input-secret materialization is a separate authority; receiving
a secret as an input does not imply permission to resolve it again or pass the
same authority to child jobs.

## Information to include

### Current execution identity

Make identity prominent in text output and available in structured fields:

| Field | Meaning |
| --- | --- |
| Tenant, goal, job ID | Identifies the current owning job; goal may be unavailable |
| Job type | Such as `recipe` |
| Logical task ID | The engine's `TaskContext.InvocationID`, scoped to the job |
| Task type | The concrete runtime task handler/type |
| Attempt number | `TaskContext.AttemptNumber`; report unknown rather than synthesizing `1` |
| Result ordinal | `TaskContext.Step`, for identifying this attempt's durable output/artifacts |
| Node ID and invocation path | Human-readable location in the recipe, including nested scopes |
| Invocation sequence/hash | Existing c2j invocation identifiers, labeled separately from the engine task ID |
| Operation type/step | Native command or extension operation and its step |
| Recipe identity | Name/source and pinned revision or digest, when the controller already knows them |
| Parent job/invocation | Direct parent identity for child jobs, when recorded; do not infer it from names |

The logical task ID survives task retries and recovery, but can change when an
outer scope or the whole job retries. The attempt number increments for a task
retry; recovery of unfinished work retains it. The result ordinal can change on
a resumed `Run`. A node ID is not globally unique. Expose these as separate fields
rather than calling all of them “task ID.”

Environment-derived identity is useful fallback information but must be labeled
unverified if the authenticated runtime cannot confirm it. Missing context means
“no current task detected,” not a fabricated job/task based on the current cell.

### Cell and workspace

Keep the current cell fields for compatibility. Add a distinct active-workspace
section containing the owning job's cell, active workspace cell and scope ID,
repository, known revision/base snapshot identity, and operation-visible paths:
`workdir`, `worktree`, `inbox`, and `outbox`.

The cell inferred from the CLI's working directory can differ from the current
job's cell or active workspace cell. Show and label the difference. Do not let a
`cd` change the reported job identity. A recorded base revision does not establish
that the current worktree is clean or identify all current file contents.

### Lease and execution lifetime

Report the authority **supporting this invocation**, not a claim that the agent
possesses the worker's full lease credential:

- Nonsecret execution lease ID and route, when available.
- Authority mode: controller-mediated, delegated credential, or embedded guard.
- Runtime-observed state: active, cancelled, lost, expired, or unknown; identify
  the source and observation time.
- Last known lease expiry, separately from the operation's hard deadline and any
  delegated `not_after` ceiling. A renewable expiry is not the maximum allowed
  task duration. No deadline reported is not a promise of unlimited lifetime.
- Renewal responsibility, normally the worker/controller; last successful renewal
  and latest renewal failure only if already tracked.
- Delegation ID and bounds only when a delegation already exists. Restricted
  leases are currently issued lazily; inspection must not mint one.

An active local context alone does not prove that JobDB still considers the
lease valid. Include `validation: last_known | verified | unavailable`, with a
`validated_at` timestamp when appropriate. Use “verified” only for an authoritative
read-only validation. Backend support may differ; missing support is explicit.
Do not call keepalive/renewal or decode private token claims merely to populate
this report.

### Effective permissions and actionable capabilities

Show both the effective operation grant and a small action table derived from
the runtime's real enforcement paths. For each action include:

- `permission`: `granted`, `denied`, or `unknown`.
- `availability`: `ready`, `unavailable`, or `unknown`.
- Supported route/command, relevant scope or allowlist, and a concise reason code
  and explanation when not ready.

`ready` means the known prerequisites and local route are present at observation
time; it does not guarantee that a later request will succeed. Actual requests
continue to validate authority. Transport failure must not become a permission
denial, and the mere existence of a helper endpoint must not become a grant.

Initial actions:

| Action | What agents need to know |
| --- | --- |
| Submit a child job | Whether `c2j submit` can use the current task's authenticated broker; inherited restrictions and supported target/job-type scope |
| Submit from incoming workspace | Whether `c2j submit --from-workspace` is supported and the task-start snapshot is available |
| Submit from current workspace | Whether `c2j submit --from-workspace-now` is supported and a usable current workspace is available |
| Resolve named secrets | Allowed names, helper availability, and supported access route; never secret values or invented CLI commands |

Report submission and execution separately: permission to submit does not imply
permission or executor availability to run a child with `--run`, `--embed`, or
another backend. Child jobs may inherit a narrower ceiling than the parent's
current operation grant. Show that ceiling when known; otherwise mark it unknown.

A controller-mediated hook submission can be allowed even when its delegated
credential has `jobs.submit` removed. Present the supported controller route as
the agent capability; do not derive the action table solely from token metadata.

Do not infer job-history reads, artifact downloads, restart/cancel/complete,
configuration access, or arbitrary delegation from a submit grant. Add those rows
only when the runtime can report the actual authority and usable route. Similarly,
JobDB permissions do not describe OS filesystem/network permissions, installed
tools, or external-service rights granted by a secret. Worker execution capability
labels belong in a separately labeled optional section.

## Example default output

Illustrative output; all new fields below are proposed:

```text
Cell:           api
Workspace:      api / scope-17
Repository:     github.com/example/api @ main

Job:            job-123 (tenant team-a, goal goal-456)
Task ID:        task-789
Task type:      extension_execution:extension_execution
Task attempt:   2
Result ordinal: 47
Invocation:     build/verify/agent
Operation:      extension_execution

Authority:      controller-mediated
Lease:          lease-abc — active (last known, observed 2026-11-03T14:00:00Z)
Lease expiry:   2026-11-03T14:01:00Z (renewable by worker)
Task deadline:  2026-11-03T14:20:00Z

ACTION                           PERMISSION  AVAILABILITY  ROUTE / SCOPE
Submit child job                 granted     ready         c2j submit; inherited child ceiling applies
Submit incoming workspace        granted     ready         c2j submit --from-workspace
Submit current workspace         granted     ready         c2j submit --from-workspace-now
Resolve secrets                  granted     ready         c2j secret get NAME; names: git_token

Use c2j self --json for identifiers, paths, permission details, and observation sources.
```

Keep text compact and deterministic. Show unavailable identity values as unknown,
not empty IDs or zero attempts. Large allowlists may be summarized in text with
an explicit count; JSON retains the complete bounded list.

## JSON additions

Preserve existing top-level cell keys. Add `schema_version`, `observed_at`,
`execution`, `workspace`, `lease`, `permissions`, `capabilities`, and `diagnostics`.
Use structured statuses rather than requiring agents to parse prose. Example
fragment, omitting existing cell fields and most workspace fields for brevity:

```json
{
  "schema_version": 1,
  "observed_at": "2026-11-03T14:00:00Z",
  "execution": {
    "status": "present",
    "source": "runtime_helper",
    "tenant_id": "team-a",
    "goal_id": "goal-456",
    "job_id": "job-123",
    "task_id": "task-789",
    "task_type": "extension_execution:extension_execution",
    "attempt_number": 2,
    "result_ordinal": 47,
    "node_id": "agent",
    "invocation_path": "build/verify/agent",
    "op_type": "extension_execution",
    "parent": null
  },
  "lease": {
    "status": "active",
    "authority_mode": "controller_mediated",
    "lease_id": "lease-abc",
    "validation": "last_known",
    "validated_at": null,
    "expires_at": "2026-11-03T14:01:00Z",
    "operation_deadline": "2026-11-03T14:20:00Z",
    "renewal_owner": "worker",
    "delegation": null
  },
  "permissions": {
    "status": "known",
    "source": "effective_operation_access",
    "jobdb": {
      "jobs": {"submit": true},
      "secrets": {"resolve": ["git_token"]}
    },
    "child_ceiling": {"status": "unknown"}
  },
  "capabilities": {
    "jobs.submit": {
      "permission": "granted",
      "availability": "ready",
      "route": "child_job_broker",
      "command": "c2j submit",
      "reason_code": null
    }
  },
  "diagnostics": []
}
```

Unknown numeric identifiers/times use `null`, not zero. Empty known allowlists
mean nothing is granted; unavailable grants use `status: unknown` with no invented
empty grant. An absent execution uses `permissions.status: not_applicable`, as
defined in the standalone examples below. `execution.status` distinguishes `present`, `absent`, and `unknown`.
A null parent/delegation means not reported, not proof that none exists. Where
that distinction matters, include a diagnostic reason such as `not_recorded` or
`delegation_not_issued`.

## Outside a lease or job: standalone mode

This is a normal supported mode, not a permission failure. When neither current-job
metadata nor an invocation helper is advertised, report `execution.status: absent`
and `lease.status: absent`. Here “absent” means no current execution context was
detected by this process; it does not claim that no jobs exist on the server.

With a resolvable cell, the default output should look like:

```text
Cell:           api
Repository:     github.com/example/api @ main
JobDB:          https://jobdb.example/team-a

Execution:      standalone — no current job or task detected
Lease:          none detected
Task workspace: none; current checkout is the api cell
Permissions:    no current-task grant; standalone JobDB permissions are unknown

ACTION                           PERMISSION  AVAILABILITY  ROUTE / SCOPE
Submit a new job                  unknown     unknown       c2j submit; configured JobDB connection
Submit incoming task workspace   unknown     unavailable   --from-workspace requires a running task
Submit current checkout          unknown     unknown       c2j submit --from-workspace-now; local checkout and JobDB access required
Resolve secrets through task     unknown     unavailable   no current-task helper

Standalone submission may still work using the configured connection.
No authentication or permission probe was performed.
```

Do not print empty `Job`, `Task ID`, `Attempt`, or deadline rows in this mode.
Do not label a normal checkout as a task workspace. The action label changes from
“Submit child job” to “Submit a new job”: the ordinary standalone route has no
current task to establish parent lineage. A configured connection is a destination,
not evidence that submission is authorized or reachable.

The corresponding JSON additions are explicit rather than omitted wholesale.
Existing cell keys remain as usual; this example includes the relevant ones:

```json
{
  "schema_version": 1,
  "observed_at": "2026-11-03T14:00:00Z",
  "short_name": "api",
  "repo": "github.com/example/api",
  "ref": "main",
  "jobdb": "https://jobdb.example/team-a",
  "execution": {
    "status": "absent",
    "source": "local_discovery",
    "tenant_id": null,
    "goal_id": null,
    "job_id": null,
    "task_id": null,
    "attempt_number": null,
    "result_ordinal": null,
    "invocation_path": null,
    "reason_code": "no_current_execution"
  },
  "workspace": {
    "status": "absent",
    "scope_id": null,
    "reason_code": "no_task_workspace"
  },
  "lease": {
    "status": "absent",
    "lease_id": null,
    "authority_mode": "none_detected",
    "validation": "not_applicable",
    "expires_at": null,
    "operation_deadline": null,
    "reason_code": "no_current_execution"
  },
  "permissions": {
    "status": "not_applicable",
    "scope": "current_execution",
    "jobdb": null,
    "reason_code": "no_current_execution"
  },
  "capabilities": {
    "jobs.submit": {
      "permission": "unknown",
      "availability": "unknown",
      "route": "configured_jobdb",
      "command": "c2j submit",
      "submission_kind": "standalone",
      "reason_code": "standalone_authority_not_inspected"
    },
    "jobs.submit_from_workspace": {
      "permission": "unknown",
      "availability": "unavailable",
      "route": null,
      "command": "c2j submit --from-workspace",
      "reason_code": "no_current_task"
    },
    "jobs.submit_from_workspace_now": {
      "permission": "unknown",
      "availability": "unknown",
      "route": "configured_jobdb",
      "command": "c2j submit --from-workspace-now",
      "reason_code": "standalone_authority_not_inspected"
    },
    "secrets.resolve": {
      "permission": "unknown",
      "availability": "unavailable",
      "route": null,
      "reason_code": "no_task_helper"
    }
  },
  "diagnostics": []
}
```

`permissions.status: not_applicable` describes the absent **current-task grant**.
It does not deny or enumerate standalone account permissions. The capability rows
refer to their stated routes; missing task-secret access says nothing about other
credentials or services. The JSON capability names above should also be used for
the equivalent rows inside a task.

### No cell configuration either

A normal shell outside a recognized cell should still get a useful answer:

```text
Cell:           unavailable — no cell configuration or supported auto-detection
Execution:      standalone — no current job or task detected
Lease:          none detected
Task workspace: none
Permissions:    no current-task grant; standalone permissions are unknown

No submission destination was resolved.
Run c2j self from a cell checkout to discover its configured destination.
```

Return exit 0 for this normal absence case. In JSON, use the same explicit absent
execution/workspace/lease and not-applicable task-permission objects as above;
unresolved cell keys are null or omitted where already optional. Include
`diagnostics: [{"code": "cell_unavailable", "message": "No cell configuration or supported auto-detection"}]`.
If no independent JobDB destination can be resolved, submission has
`permission: unknown`, `availability: unavailable`, `route: null`, and
`reason_code: no_submission_destination`. Do not invent a default remote target.
If an environment/default connection does resolve independently, display it and
retain unknown standalone authority, even though the cell remains unavailable.

### Context advertised but not inspectable

A process with current-job metadata or helper configuration is **not** the normal
standalone case. Preserve readable job/task metadata as unverified, report lease
state and permissions as unknown, and explain the failure (`helper_unavailable`,
`inspection_unsupported`, or `incomplete_execution_context`). If job identity is
readable, `execution.status` can be `present` with `source: environment_unverified`;
if it cannot be established, use `unknown`. Never switch its action rows to the
standalone connection as a fallback. This prevents a temporarily unreachable
worker from being mistaken for “no restrictions.”

## Runtime discovery and failure behavior

1. Resolve existing cell information independently from execution discovery.
   Missing/invalid project config must not hide valid current-task information.
2. Read protected current-job metadata as a fallback identity source.
3. Query an authenticated, invocation-bound, read-only self endpoint from the
   runtime helper. The endpoint is `GET /v1/child-jobs/self`, reusing the
   existing broker transport/session authentication. It must be available even
   when submission is denied; inspection is not a submission operation.
4. The controller supplies current `TaskContext` identity, workspace paths,
   `permissions.Access.Grant()`, effective deadline/cancellation state, and safe
   lease metadata from the live runtime. Snapshot immutable facts and fetch
   mutable lifetime/status from a live provider on each request.
5. If no broker exists for a supported operation, expose equivalent inspection
   independently of submit capability. Never widen the grant to enable discovery.

Use a short bounded timeout (two seconds) and a bounded response body (512 KiB).
No retries that make a routine agent discovery command hang. Treat an older
broker's unsupported endpoint as `inspection_unsupported`; preserve locally known
identity and report permissions/lease validity as unknown. A closed helper after
cancellation is `helper_unavailable`, not proof of a specific lease failure.

Outside a task, retain the ordinary cell report and explicitly say that no current
execution authority is known. Never describe ambient credentials as unrestricted
access. If advertised context is incomplete, authentication fails, or returned
job/invocation identity conflicts with local metadata, report the mismatch and
avoid asserting usable authority. Do not fall back to a different JobDB connection
as though it were the current task's lease.

For ordinary `self`, return exit 0 when a useful full or partial report was
produced; encode missing sections and reasons in the output. Normal absence of both task and cell context is itself a useful report and
returns exit 0. Return nonzero for usage/output errors or failures that prevent
producing a report. Authorization
consumers must inspect structured statuses, not the process exit code. No
additional strict/check flags are required for the initial delivery.

Inspection must not resolve secrets, submit probe jobs, create delegations,
acquire/renew leases, replay a task, or execute configuration commands solely to
prove capability. Runtime discovery should work even if cell configuration
resolution fails. Cancellation from the CLI must propagate to discovery; use the
Cobra command context rather than the current `context.Background()`.

## Safe disclosure

Return IDs and allowlisted metadata, never bearer tokens, broker tokens/session
credentials, credential files, secret values, complete environments, or lease
client payloads. Reuse runtime redaction and sanitize configured JobDB/repository
URLs (including userinfo and query credentials). This applies to existing `self`
fields, new fields, error messages, text, and JSON. Escape control characters in
text metadata. Do not include raw network errors or credential-bearing request
objects in diagnostics.

Secret names in the effective allowlist are useful to the executing agent; do not
query the provider to enumerate unrelated names or test whether granted names
currently have values. The authenticated endpoint returns information only for
its bound invocation, not a caller-selected job/tenant/task.

## Delivery and acceptance

Implement this in c2j's runtime/CLI; agents and extension manifests need no new
inputs, outputs, or setup steps. Document the command in README and agent guidance
only when it is implemented. Initial scope is identity, workspace, lifetime,
effective grants, and the four action rows above. General IAM enumeration and
permission-changing commands are outside this proposal.

Acceptance tests should cover:

1. Both command and extension subprocesses discover their correct tenant/job/task,
   node path, attempt, result ordinal, and workspace without author changes.
2. Task retry changes the attempt while preserving logical task identity; recovery
   and outer-scope retry follow the engine's actual identity semantics.
3. `cd`, nested includes, cross-cell workspaces, and child jobs do not confuse the
   current execution with the cell inferred from the filesystem.
4. Effective limits/defaults and separate input-secret authority are represented
   correctly. A broker existing with submission denied still answers inspection.
5. Controller-mediated submission is reported correctly when token-level submit
   is absent, including inherited child restrictions.
6. Renewals update observed expiry; operation deadlines, cancellation, and lease
   loss update the report. Inspection never renews, mints, or submits anything.
7. Old/unreachable helpers, absent task context, config failures, and identity
   mismatches produce explicit partial/unknown results without authority fallback.
   Standalone cases with and without a cell match the examples above, return exit
   0 for normal absence, and never turn unknown standalone authority into denial
   or a grant. Advertised but unavailable execution context never becomes standalone.
8. Embedded and remote runtimes expose only facts they can establish; last-known
   status is never labeled authoritatively verified.
9. Tokens/secret values and URL credentials never appear in text, JSON, or errors.
10. Existing JSON cell keys remain compatible; new fields and reason codes have
    deterministic, versioned tests, including unknown vs empty vs denied states.

### Delivery notes

The runtime provider, authenticated broker endpoint, CLI rendering, and agent
instructions are implemented. Examples above illustrate the contract; JSON may
contain additional nullable identity fields and reason codes. The implementation
reports `validation: last_known` and `validated_at: null`; it does no authority
probe. It distinguishes an existing delegated credential from the controller's
effective grant and includes imported parent bounds as `permissions.parent_bounds`.

Recipe operation retries create a new task under the next scope attempt. Its
engine task attempt can therefore remain 1. Text labels this as **Task attempt**;
`attempt_number` is always the actual engine value. Missing parent identity remains
`null`: the current operation context does not carry the parent's job metadata.
Recipe source/ref are reported; unrecorded digests and worktree content identity
are not inferred. Secret resolution uses the existing `c2j secret get NAME` route.

Tests exercise command and extension subprocesses on embedded and remote backends,
operation retries, replay with the original repository offline, denied submission
with read-only inspection, lease renewal/cancellation/loss, delegation laziness,
standalone/partial context, config failures, helper failures, identity mismatch,
and credential redaction. Unit tests also check engine task attempt/ordinal values
independently of recipe scope retry semantics.
