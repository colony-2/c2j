# Proposal: c2j Nested Job Context and Deferred Submit/Await

## Summary

c2j does not currently have a general CLI/process-level pattern for detecting
that it is running inside an existing SWF job or task.

The existing job-context mechanisms are internal:

- `swf.JobContext` and `swf.TaskContext` carry the current job key while the
  in-process worker is running.
- `pkg/ops.JobTool` wraps `swf.TaskContext` for recipe ops, exposing
  `GetJobKey()` and `AwaitJobs(...)`.
- `recipe.run_and_get_result` and `recipes.run_and_wait` already start child
  jobs from inside a job and use `AwaitJobs`, which SWF implements as a durable
  reschedule when dependencies are not complete.
- `c2j submit` itself does not inspect a current-job environment, and command
  or extension subprocesses are not given a c2j-specific current-job contract.

We should add:

1. A job-context environment contract for subprocesses.
2. Parent/child metadata on jobs started from another job.
3. A `submit and await` command mode that, when called from inside a c2j task,
   defers the submission to the framework and turns the wait into a SWF
   job-level reschedule.

## Goals

- A subprocess can reliably tell it is running under c2j job execution.
- The same detection works in host execution and sandbox execution.
- Jobs started from jobs get queryable metadata such as `parent_job_id`.
- An agent loop inside a task can call c2j to request child work without
  burning the current lease while polling.
- The parent job should not rerun the agent task after the child wait if the
  task already completed successfully.
- Direct CLI usage outside a job should keep working as a normal submit/wait.

## Current State

The current CLI env vars are runtime defaults only:

- `C2J_SWF_URL`
- `C2J_TENANT_ID`
- `C2J_EMBED_ROOT`

Those do not prove that c2j is executing inside a live job. They are also valid
for normal local CLI usage.

The worker path does have enough information while executing an op:

- `taskWorker.Run` receives `swf.TaskContext`.
- `TaskContext.JobKey` contains tenant and job id.
- `TaskContext.AwaitJobs` routes to the SWF runner.
- The runner reschedules the job with `WaitForJobIDs` when dependencies are not
  complete.

The missing part is making a constrained version of this context visible to
child processes and giving c2j a place to write submit/await requests that the
framework can apply after the subprocess exits.

## Job Context Detection

### Recommended Contract

Set these env vars for command and extension subprocesses:

```text
C2J_JOB_CONTEXT=<compact JSON>
C2J_JOB_CONTEXT_FILE=<path visible to the subprocess>
C2J_DEFERRED_ACTIONS_DIR=<path visible to the subprocess>
```

`C2J_JOB_CONTEXT` should be small and non-secret:

```json
{
  "v": 1,
  "tenant_id": "tenant-123",
  "job_id": "job-a",
  "task_ordinal": 4,
  "task_type": "command_execution:execute",
  "cell": "github.com/acme/app",
  "invocation": {
    "path": "deploy/run-agent",
    "sequence": 1,
    "hash": "abcd1234"
  }
}
```

`C2J_JOB_CONTEXT_FILE` should contain the same JSON. It gives tools a stable
path when env size or shell quoting is inconvenient.

`C2J_DEFERRED_ACTIONS_DIR` is the important control channel. If present, c2j
knows it may write deferred framework requests instead of performing every
operation directly. The path must be inside the operation workdir and mounted
into sandbox environments.

The env contract should be treated as a local execution hint, not an authority
boundary. Authoritative parent metadata must be filled by the framework from
the real `swf.TaskContext`, not trusted from a user-supplied env var.

### Sandbox Handling

Use a dedicated control directory under the operation workdir, for example:

```text
<host operation workdir>/.c2j/control/deferred-actions
```

The worker should expose the path in the subprocess view:

- host execution: `C2J_DEFERRED_ACTIONS_DIR=<host workdir>/.c2j/control/deferred-actions`
- shai sandbox: `C2J_DEFERRED_ACTIONS_DIR=<sandbox workdir>/.c2j/control/deferred-actions`

This works with the existing process runtime shape because shai receives an env
map and the operation workdir is already part of the mounted path set.

### Lease Token Option

There is a tempting alternative: expose the current SWF lease id/token to the
subprocess and let `c2j submit --await` call SWF reschedule directly.

That is not the recommended default:

- `swf.TaskContext` currently does not expose a lease id or remote lease token.
- Remote SWF has a lease token internally, but it is not part of the public
  `swf.ExecutionLease` interface or the task context.
- Direct/sqlite runtime does not need the same token shape.
- Putting a live mutation credential in every command or extension environment
  gives arbitrary subprocess code the ability to reschedule or complete the
  parent job.
- Remote lease tokens are short-lived and need refresh/keepalive behavior.

If we need a direct-control mode later, add an explicit, opt-in capability:

```text
C2J_JOB_LEASE_ID=<lease id>
C2J_JOB_LEASE_TOKEN=<short-lived token>
C2J_JOB_LEASE_WORKER_ID=<worker id>
```

That would require SWF-go API support and should be reserved for trusted
subprocesses. The deferred-actions directory is safer and works for host and
sandbox execution without handing out lease credentials.

## Parent/Child Job Metadata

Current recipe job metadata contains:

```go
v
recipe
cell_id
cell_name
git_ref
```

Add optional fields to `starter.JobMetadata`:

```go
ParentTenantID     string `json:"parent_tenant_id,omitempty"`
ParentJobID        string `json:"parent_job_id,omitempty"`
ParentTaskOrdinal  *int64 `json:"parent_task_ordinal,omitempty"`
ParentTaskType     string `json:"parent_task_type,omitempty"`
ParentInvocationID string `json:"parent_invocation_id,omitempty"`
ParentNodePath     string `json:"parent_node_path,omitempty"`
StartedBy          string `json:"started_by,omitempty"`
```

Suggested metadata field names for filters:

```go
MetaFieldParentJobID       swf.FieldName = "parent_job_id"
MetaFieldParentInvocation  swf.FieldName = "parent_invocation_id"
MetaFieldStartedBy         swf.FieldName = "started_by"
```

`started_by` values could be:

- `recipe_op`
- `c2j_deferred_submit`
- `c2j_direct_submit`

Implementation points:

- Extend `workflowctl.StartJob` with a `Parent *JobParentContext` field.
- Extend `starter.JobMetadataFromStartJob`.
- In `pkg/ops/recipe/launcher.go`, fill parent metadata from the current
  `JobTool` and invocation. This covers existing recipe child jobs.
- In deferred c2j submissions, ignore any parent fields in the intent and fill
  them from the framework's real task context.
- In direct `c2j submit` inside a detected env context, parent metadata can be
  best-effort only unless a signed/validated context is added later.

## Deferred Submit/Await Command

Add a command mode:

```bash
c2j submit --await ...
```

or a clearer alias:

```bash
c2j submit-and-await ...
```

Outside a job context, this should submit immediately and then behave like
`c2j run --job-id <id> --on-not-ready wait`.

Inside a c2j task context with `C2J_DEFERRED_ACTIONS_DIR` set, it should not
submit immediately. Instead it writes a normalized intent file and exits
successfully.

### Intent Schema

One file per request, written by atomic rename:

```json
{
  "v": 1,
  "kind": "submit_and_await",
  "idempotency_key": "job-a/4/0",
  "planned_job_id": "deterministic-child-id",
  "recipe": "child-recipe",
  "cell": "github.com/acme/app",
  "git_ref": "main",
  "inputs": {
    "prompt": "continue analysis"
  },
  "artifacts": [],
  "requested_at": "2026-05-18T00:00:00Z"
}
```

The CLI should print a normal machine-readable response so an agent can record
the planned child id:

```json
{
  "deferred": true,
  "await": true,
  "job_id": "deterministic-child-id"
}
```

The intent should store normalized c2j options, not an arbitrary shell command.
That avoids command injection and makes validation deterministic.

### Idempotency

The planned child job id must be deterministic. A good default is derived from:

- parent tenant id
- parent job id
- task ordinal
- invocation hash/path
- per-task intent index or caller-provided idempotency key
- normalized submit payload hash

This mirrors the existing `deterministicChildJobID` pattern in
`pkg/ops/recipe/launcher.go`.

Idempotency matters because there are unavoidable crash windows:

- child job submitted but task output not persisted
- task output persisted but parent reschedule fails
- parent resumes and replays the same task output

SWF submit reconciliation plus deterministic job ids should make retries safe.

## Framework Flow

Recommended flow:

1. `taskWorker.Run` creates a control directory and injects the job-context env
   into command/extension process execution.
2. The agent subprocess calls `c2j submit --await ...`.
3. c2j detects `C2J_DEFERRED_ACTIONS_DIR`, writes a `submit_and_await` intent,
   prints the planned child job id, and exits.
4. The op finishes normally.
5. The task worker reads and validates deferred intents.
6. The task worker submits child jobs through `WorkflowControl.StartJob`, using
   deterministic job ids and authoritative parent metadata from `swf.TaskContext`.
7. The task worker attaches the submitted child job ids to
   `ActivityInvocationOutput`, for example:

   ```go
   DeferredJobs []DeferredJob `json:"deferred_jobs,omitempty"`
   ```

8. SWF persists the normal task output chapter.
9. `DefaultRecipeExecutor.executeOp2` receives the persisted task output,
   updates normal op state, then calls `ctx.AwaitJobs(childIDs...)`.
10. SWF reschedules the parent job at the job level with `WaitForJobIDs`.
11. When the children complete, the parent resumes from the already persisted
    task output and continues to the next recipe node. The agent task is not
    rerun.

The key detail is that the wait happens after the task output chapter exists.
That keeps arbitrary agent loops from rerunning after the wait.

## Alternatives

### A. Direct Submit And Direct Reschedule From The CLI

The CLI could submit the child and call a lease-token-backed reschedule API.

Pros:

- Simple mental model for the subprocess.
- No intent collection step.

Cons:

- Requires SWF-go to expose lease credentials to task subprocesses.
- Harder to support uniformly across remote and embedded/sqlite runtimes.
- Leaks powerful parent-job mutation credentials into arbitrary command env.
- More sensitive to lease-token expiration.

This should not be the default.

### B. Task-Level `AwaitJobs` From The Task Worker

The task worker could submit child jobs and immediately call
`swf.TaskContext.AwaitJobs`.

Pros:

- Requires fewer compiler/job-worker changes.
- Uses existing SWF task await behavior.

Cons:

- If the task output is not persisted before the wait, the task will rerun
  after the child jobs complete.
- That is poor behavior for long-running agent loops and non-idempotent tools.

This is acceptable only for explicitly replay-safe ops.

### C. Existing Recipe Child Ops Only

Require users to express child jobs through `recipe.run_and_get_result` or
`recipes.run_and_wait`.

Pros:

- Already close to the desired durable SWF behavior.
- Deterministic child ids already exist.

Cons:

- Does not help an agent subprocess that decides dynamically to call c2j.
- Does not provide a general CLI-level job-context detection pattern.

This remains useful but does not solve the requested agent-loop case.

## Implementation Outline

1. Add `pkg/jobcontext` or similar with:
   - env var constants
   - context JSON structs
   - parser for `C2J_JOB_CONTEXT` / `C2J_JOB_CONTEXT_FILE`
   - intent structs and atomic writer/reader helpers
2. Extend worker execution:
   - create the control directory per op execution
   - inject context env into command and extension `process.RunRequest.Env`
   - preserve sandbox-visible paths through `OperationPathRuntime`
3. Extend deferred intent handling:
   - parse and validate all intent files after the op subprocess completes
   - reject unsupported flags or missing recipe/cell data
   - submit through `WorkflowControl.StartJob`
   - attach submitted child ids to task output
4. Extend `ActivityInvocationOutput` and strict decoding.
5. In `DefaultRecipeExecutor.executeOp2`, after decoding a successful activity
   output and updating op state, call `ctx.AwaitJobs(...)` for any deferred
   await jobs.
6. Add parent metadata to `workflowctl.StartJob`, `starter.JobMetadata`, and
   existing recipe child launchers.
7. Add CLI support:
   - `c2j submit --await` or `c2j submit-and-await`
   - outside job: submit + `run` wait
   - inside job with deferred dir: write intent and return planned id
   - optional `--idempotency-key` for agents that want stable semantic ids
8. Add list/filter affordances:
   - expose parent fields in JSON list output
   - optionally add `c2j list --parent-job-id <id>`

## Open Questions

- Should the command name be `submit --await`, `submit --wait`, or
  `submit-and-await`? `await` is more precise because inside a job it means SWF
  reschedule, not local polling.
- Should `submit --await` return child outputs to the agent process? The
  deferred form cannot synchronously return outputs without blocking the parent
  lease, so the initial version should return only the planned job id.
- Should deferred child results be automatically added to recipe state after
  the wait? The first version can only wait. A later `submit --await-result`
  could fetch child outputs after resume and add them to a known output shape.
- Do we need a signed context token to prevent spoofed parent metadata in direct
  CLI submits? Framework-submitted deferred jobs do not need this because the
  framework fills parent metadata itself.

## Recommended Path

Implement the deferred-actions directory, parent metadata, and
`c2j submit --await` first. Do not expose lease tokens by default.

This gives agents a simple command surface while keeping SWF ownership inside
the worker framework:

```bash
c2j submit --await --recipe child --inputs-json '{"prompt":"continue"}'
```

Outside a job, the command behaves like normal submit plus wait. Inside a c2j
task, it records an intent; the framework submits child jobs after the task
process exits, persists the task output, and reschedules the parent job waiting
for those child ids.
