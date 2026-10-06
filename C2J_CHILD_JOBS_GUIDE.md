# Parent and child jobs in c2j

This guide describes the implemented behavior for recipe authors, command and
extension authors, and operators. It replaces the historical plans as the usage
reference; it does not propose new functionality.

Parent/child subprocess workflows are **not generally supported in embedded JobDB**.

## 1. What happens automatically

When a running recipe invokes a command or extension, c2j supplies environment
variables identifying the current job and op invocation. It also starts a
short-lived child-submission broker when the execution context supports
lease-scoped submission.

If that process, or a subprocess that inherits its environment, runs
`c2j submit`, c2j submits through the broker. JobDB records the running job as
the new job's formal parent, and c2j records which op invocation launched it.

```text
Parent recipe's op → nested c2j submit → parent-side broker → JobDB child job
```

No parent flags, special recipe inputs, or manually constructed metadata are
needed for this normal workflow. It applies to build, evolve, and advanced
recipe submissions, including another target cell in the same tenant.

The relationship belongs to jobs, not operating-system processes. Two nested
submissions from the same command are siblings. When one of those child jobs
later executes an op and submits another job, that new job is its child.

Submission does not automatically execute the child or make the parent wait.
Workers must run the submitted jobs; add an explicit wait when the parent
depends on their results.

## 2. Prerequisites

- Run the parent through a c2j worker connected to your JobDB service.
- Make the `c2j` executable available on `PATH` in the command or extension's
  execution environment, including any externally provisioned worker image.
- Preserve the injected `C2J_CURRENT_*` and `C2J_CHILD_JOB_*` variables when
  launching subprocesses.
- Configure the nested CLI's JobDB target and use the same tenant as the
  parent. Supply `--jobdb`, `C2J_JOBDB`, or project configuration.
- Ensure workers have access to the target repository and required recipes.

For example, configure a remote runtime in the shell where you use c2j:

```bash
export C2J_JOBDB=https://jobdb.example.com/dev
```

The precedence is `--jobdb`, then `C2J_JOBDB`, then the project's `jobdb`
configuration. The injected current-job variables do **not** configure the
JobDB URL. In particular, `C2J_TENANT_ID` alone is not sufficient.

Brokered submission goes to the parent's runtime. Listing and running jobs
connect through the CLI's configured JobDB URL, so configure the actual parent
service, not just another service with the same tenant name.

## 3. Submit from inside a command or extension

With the inherited environment and JobDB configuration in place:

```bash
# Build is the default.
c2j submit "Implement the supporting endpoint"

# Select evolve explicitly.
c2j submit "Improve retry handling" --evolve

# Another target cell, still within the parent's tenant.
c2j submit "Update the client" --cell github.com/acme/client

# A custom recipe in the target cell.
c2j submit "Review the API changes" --advanced-recipe review --json

# Embed a local recipe and attach a file available to this process.
c2j submit "Review the attached brief" \
  --advanced-recipe-file ./review.yaml \
  --artifact brief=./brief.md \
  --json
```

The same parent tracking applies to all of these forms. Local recipe content
and attached artifact bytes are carried through the broker; the parent worker
does not need to open the child process's file paths.

Every CLI submission needs a non-empty prompt. Pass it positionally or as
`prompt` in `--inputs-json`/`--inputs-file`, not both. Do not rely on interactive
prompting inside an op: non-terminal submissions without a prompt fail.

Build/evolve submissions pass these recipe inputs:

```json
{"prompt": "Implement the supporting endpoint", "type": "build"}
```

Evolve uses `"type": "evolve"`. A supplied type must match the selected mode.
Custom names, explicit Git selectors, and local files do not receive an
automatic type. See [the submission contract](README.md#buildevolve-input-contract).

`--json` returns the submitted identity, not the recipe's eventual output:

```json
{"tenant_id": "dev", "job_id": "child-job-id", "recipe": "build"}
```

For custom scripts, capture this JSON with a JSON parser rather than parsing
the human-readable submission message. Within recipe logic, use the recorded
`jobs` context described below instead of parsing stdout.

### Choosing the target cell

Without `--cell`, nested submission resolves the current cell using the nested
process's working directory and normal project configuration. Parent tracking
does not replace target-cell resolution or automatically select a recipe ref.

Use an explicit `--cell` when the working directory is ambiguous. In a recipe,
`context.git.repo` identifies the target repository. Do not assume
`C2J_CURRENT_REPOSITORY_SOURCE` always identifies that target: it prefers the
recipe-source repository, which may be the shared recipe repository.

Build/evolve names resolve in the target cell at its configured ref, with the
documented shared fallback. Named recipes use committed content. For local
recipe changes that have not been committed, use `--advanced-recipe-file`.

## 4. Complete command-based example

This example submits one child and returns its identity. It deliberately does
not wait for the child. It needs no shared build/evolve recipes.

Save this child as `.c2j/recipes/child-example.yaml` in the target repository:

```yaml
id: child-example
version: "1.0.0"
input_schema:
  prompt:
    type: string
    required: true
inputs:
  prompt: "{{ inputs.prompt }}"
sequence: []
outputs:
  received_prompt: "{{ inputs.prompt }}"
```

Commit that file to the ref c2j uses for this target. Save the following parent
as `parent-example.yaml`; the parent can be submitted as an uncommitted file:

```yaml
id: parent-example
version: "1.0.0"
input_schema:
  prompt:
    type: string
    required: true
  jobdb:
    type: string
    required: true
inputs:
  prompt: "{{ inputs.prompt }}"
  jobdb: "{{ inputs.jobdb }}"
sequence:
  - id: launch
    op: command_execution
    inputs:
      working_directory: "{{ context.environment.op.worktree_path }}"
      env:
        C2J_JOBDB: "{{ inputs.jobdb }}"
        CHILD_CELL: "{{ context.git.repo }}"
        CHILD_PROMPT: "{{ inputs.prompt }}"
      run: |
        c2j submit "$CHILD_PROMPT" \
          --advanced-recipe child-example \
          --cell "$CHILD_CELL" \
          --json
outputs:
  child_job_ids: "${{ has(sequence.launch.jobs.job_ids) ? sequence.launch.jobs.job_ids : [] }}"
  children: "${{ has(sequence.launch.jobs.items) ? sequence.launch.jobs.items : [] }}"
```

The prompt is passed through a quoted shell variable, not interpolated into
shell code. The runtime adds protected parent/broker variables alongside this
explicit `env` map.

From a directory that resolves as the target cell:

```bash
export C2J_JOBDB=https://jobdb.example.com/dev

c2j submit "Review the new endpoint" \
  --advanced-recipe-file ./parent-example.yaml \
  --inputs-json '{"jobdb":"https://jobdb.example.com/dev"}' \
  --json
```

Save the returned parent job ID. If workers are not already running, start a
worker connected to the same runtime:

```bash
c2j run loop --jobdb https://jobdb.example.com/dev
```

The parent launches the child when its command executes; workers can then
execute the child. Both may finish quickly, so include terminal statuses when
checking the relationship:

```bash
c2j list children \
  --parent-tenant-id dev \
  --parent-job-id <parent-job-id> \
  --all-ops --all \
  --status READY,EXPIRED,PENDING_JOBS,AWAITING_FUTURE,ACTIVE,CRASH_CONCERN,COMPLETED,CANCELLED \
  --json
```

## 5. List children

### Inside the launching op

```bash
c2j list children --json
```

The parent tenant, parent job ID, and invocation hash default from the current
environment. By default, this selects children launched by **this op
invocation**, not every invocation of the same op type.

To include other op invocations in the same parent job:

```bash
c2j list children --all-ops --json
```

A later op has a different invocation hash. Use `--all-ops` there to find
children launched by earlier steps.

### Outside a running job

Specify the parent explicitly; the flag is `--parent-job-id`, not `--job-id`:

```bash
c2j list children \
  --jobdb https://jobdb.example.com/dev \
  --parent-tenant-id dev \
  --parent-job-id <parent-job-id> \
  --all-ops --json
```

For one particular invocation, replace `--all-ops` with
`--parent-invocation-hash <hash>`.

### Statuses, pagination, and scope

- Only direct child **recipe jobs** are returned, not the whole descendant
  tree. Query a child's ID as a parent to inspect another generation.
- Default statuses are `READY`, `EXPIRED`, `PENDING_JOBS`, `AWAITING_FUTURE`,
  `ACTIVE`, and `CRASH_CONCERN`. Terminal jobs are excluded by default.
- To see terminal jobs, add `--status COMPLETED,CANCELLED`. Explicit status
  filters replace the default set; repeat `--status` or use comma-separated
  values to combine states. Use the full set shown above to see both active
  and terminal jobs.
- `--all-ops` removes the invocation filter. It does not change status filters
  or fetch additional pages.
- `--all` fetches every page. It does not mean all statuses, all invocations,
  or all descendants.
- Use `--page-size` and `--page-token` for manual pagination. JSON includes
  `next_page_token` when another page is available.
- `--created-after` and `--created-before` accept RFC3339 timestamps.
- Execution-allocation filters are also available through
  `--compatible-with-execution` and the execution flags; see
  `c2j list children --help` for the current options.

JSON has a top-level `jobs` array. Each job includes identity, recipe, status,
store, timestamps, execution information, and a `parent` object when known.
For example, this is an excerpt, not the full response:

```json
{
  "jobs": [
    {
      "tenant_id": "dev",
      "job_id": "child-job-id",
      "recipe": "child-example",
      "status": "READY",
      "parent": {
        "tenant_id": "dev",
        "job_id": "parent-job-id",
        "invocation_path": "sequence.launch",
        "invocation_hash": "invocation-hash"
      }
    }
  ]
}
```

The child selection uses JobDB's formal parent relationship. A displayed
`parent` object alone is not proof of that relationship: older or metadata-only
submissions may carry c2j attribution without a formal JobDB parent.

## 6. Access submitted children from recipe logic

After a command or extension invocation finishes, c2j records the jobs it
started under a `jobs` sibling of `outputs` and `artifacts`:

```text
sequence.launch.jobs.job_ids
sequence.launch.jobs.items
states.launch.jobs.job_ids
states.launch.jobs.items
```

Each item can contain `tenant_id`, `job_id`, `recipe`, `status`, and
`parent_invocation_hash`. Status can be absent and is a snapshot, not a live
view. Do not use collection ordering as a durable child key.

This is runtime-provided context; command stdout and extension output schemas
do not need a `jobs` field. The collector includes active and archived children,
so a child that finishes before the launching op ends can still be recorded.
For an invocation with no children, treat the collection as empty; serialized
empty fields may be omitted. Use `has(...)` guards as in the complete example
when reading these fields, including during validation where no actual child
submissions have occurred. Check that an ID list is non-empty before indexing it.

The runtime attempts collection on failed op execution too, but a failed or
interrupted op is not a guarantee of a usable downstream output. Use explicit
child listing to investigate submissions made before a failure.

## 7. Waiting, results, and native recipe children

Parentage and waiting are separate. To wait for a known child job, use a
`recipe.await_result` node with `inputs.job_id` set to that child's ID. It
suspends the parent as needed and returns a wrapper whose `outputs` field
contains the child's recipe outputs. A failed or cancelled child causes the
normal await path to fail.

Use `recipe.await_result_soft` when failure or cancellation should be data:

```yaml
- id: inspect_child
  op: recipe.await_result_soft
  inputs:
    job_id: "{{ inputs.child_job_id }}"
    return_when: terminal
```

This is a node fragment for a recipe that declares and binds `child_job_id`.
The returned fields include `job_id`, `terminal`, `status`, failure details,
and available outputs/artifacts. `return_when: current_status` inspects without
waiting for terminal state. Do not pass `timeout` or `poll_interval` to this
op; the current implementation rejects them.

For declarative orchestration, native recipe operations do not require a
nested CLI or manually supplied environment variables:

| Operation | Behavior |
|---|---|
| `recipe.run_and_get_result` | Start one child, wait, and retrieve outputs. |
| `recipes.run` | Start children and return their job IDs. |
| `recipes.run_and_wait` | Start children and wait. |
| `child_group` | Structured fan-out with keyed children and aggregation. |
| `recipe.get_result` | Retrieve an already-finished recipe's output. |

These launches also use lease-scoped child submission in normal worker
execution and participate in the same child listing. Native child starts have
deterministic IDs based on the parent invocation and child position/key.

Native recipe operations are not CLI submissions: supply their recipe inputs
explicitly. If calling a build/evolve recipe that requires `prompt` and `type`,
pass both; the CLI's automatic type injection does not apply to native ops.

Workers must be available to execute children while the parent is waiting.
Running just the parent with `c2j run --job-id ...` is not a substitute for
workers that can claim its children. Prefer submitting asynchronously and
waiting in recipe logic rather than holding an op open with a nested
`c2j submit --run`.

## 8. Environment reference

These variables describe the currently executing job/op. A nested submission
uses that context to describe its parent.

| Variable | Meaning |
|---|---|
| `C2J_CURRENT_CONTEXT_VERSION` | Emitted contract version, currently `1`. |
| `C2J_CURRENT_TENANT_ID` | Current job's tenant; required for parent context. |
| `C2J_CURRENT_JOB_ID` | Current job's ID; required for parent context. |
| `C2J_CURRENT_JOB_TYPE` | Job type, normally `recipe`; not the build/evolve input type. |
| `C2J_CURRENT_OP_TYPE` | Executing operation type. |
| `C2J_CURRENT_OP_STEP` | Operation's internal execution step. |
| `C2J_CURRENT_OP_TASK_TYPE` | Executing task type. |
| `C2J_CURRENT_CELL_NAME` | Current cell name, when available. |
| `C2J_CURRENT_REPOSITORY_SOURCE` | Recipe-source repository, falling back to the base repository. |
| `C2J_CURRENT_GIT_REF` | Recipe-source ref, falling back to the base ref. |
| `C2J_CURRENT_INVOCATION_PATH` | Recipe node invocation path. |
| `C2J_CURRENT_INVOCATION_SEQUENCE` | Integer invocation sequence. |
| `C2J_CURRENT_INVOCATION_HASH` | Invocation identifier used for attribution and listing. |
| `C2J_TENANT_ID` | Tenant convenience variable for other tools; not CLI JobDB configuration. |

Optional context values are omitted when unavailable. Once current-job context
is present, both current tenant and current job ID must be non-empty. Partial
context and malformed invocation sequences cause errors rather than silently
dropping attribution.

The broker supplies a second group:

| Variable | Meaning |
|---|---|
| `C2J_CHILD_JOB_ENDPOINT` | Endpoint for the current op's parent-side broker. |
| `C2J_CHILD_JOB_TOKEN` | Secret bearer token for that broker session. |
| `C2J_CHILD_JOB_SESSION_ID` | Broker session identifier. |

All three broker variables are required together. Broker context also requires
valid current-job context. The broker owns the parent identity and replaces
client-supplied attribution with its current parent context.

### Metadata-only submission versus a formal child

| Environment | Submission behavior |
|---|---|
| No current-job or broker context | Ordinary top-level submission. |
| Valid current-job context, no broker | Top-level submission with c2j parent-attribution metadata; not a formal JobDB child. |
| Valid current-job and broker context | Lease-scoped submission with formal parentage and op attribution. |
| Partial context or an unusable advertised broker | Error; no silent fallback to top-level submission. |

Manually exporting parent IDs does not grant access to the parent's lease or
make a job appear in formal child listing. Attribution metadata is not an
authentication mechanism. `c2j submit` has no parent-ID flag or explicit
detached-submission flag; use a clean, independent environment when you
intentionally want an unrelated top-level job.

## 9. Extension tools and worker environment

Command execution and selector-backed extensions receive the same protected
context. Runtime values override conflicting entries in the command's `env`
or the extension's environment configuration.

For wrapper scripts and tools:

- Preserve the inherited context and broker environment. A tool that builds a
  fresh subprocess environment must forward the required groups explicitly.
- Do not print the broker token, dump the entire environment, or store broker
  credentials in artifacts, recipe inputs, or durable configuration.
- Do not copy a broker session to another job. The broker is scoped to the
  current op and closes when that invocation ends.
- Finish child submission before returning from the op. Background processes
  that outlive the op cannot rely on its broker or lease remaining available.
- Treat the session token as permission to submit through that parent. It is
  not the JobDB lease token; raw lease credentials remain in the parent worker.

Operations and their child CLI processes run in the worker environment. The
broker listens on loopback with a fresh dynamic port for each invocation; this
also works when the entire worker runs in an externally provisioned container.
Install the CLI and its runtime dependencies in that environment. Preserve the
injected endpoint, token, and session ID. c2j does not expose the broker through
a container gateway or grant extra network access to independently launched tools.

## 10. Retries and failure handling

Nested CLI submissions receive generated job IDs. Repeating a successful
`c2j submit` creates another job; the shared invocation hash is attribution,
not an idempotency key. There is no CLI `submit --job-id` option.

Do not blindly retry a submission after an ambiguous transport failure: the
broker may already have accepted it. Inspect children for the relevant parent
invocation, including terminal statuses, and reconcile your application-level
intent. Prefer native recipe child operations when deterministic replay-safe
child starts are important.

A command failure after submission does not undo the child creation. Parent
tracking alone does not establish a wait, propagate child failure into the
parent, or provide a cancellation policy; express those orchestration choices
explicitly.

## 11. Troubleshooting

| Symptom | Check |
|---|---|
| `--jobdb is required` | Configure `--jobdb`, `C2J_JOBDB`, or project `jobdb`; current tenant variables do not provide a URL. |
| Current tenant/job variables required | Preserve the complete current-job context; do not forward only selected op fields. |
| Child broker variables required | Forward endpoint, token, and session ID together. |
| Broker connection refused or authorization rejected | Check session lifetime and worker-local reachability; do not reuse credentials from another invocation. |
| Tenant mismatch | Parent-linked submission and listing must use the parent's tenant. |
| Missing prompt or unknown `prompt`/`type` input | Pass a prompt explicitly and declare the relevant input schema; build/evolve recipes receive both fields. |
| No child jobs listed | Check runtime/tenant, parent ID, invocation scope, status filters, pagination, and whether submission used the broker rather than metadata alone. |
| Invocation hash required when listing | Supply `--parent-invocation-hash`, or use `--all-ops`. |
| A later step sees no children | Its invocation hash differs; use `--all-ops` or read the earlier step's `jobs` context. |
| A child disappeared after finishing | Include `COMPLETED` and `CANCELLED`; default listing is nonterminal only. |
| Parent waits indefinitely | Ensure workers can claim the children and satisfy their task/execution requirements. |
| Duplicate children after retry | CLI submits are not automatically deduplicated; reconcile before resubmitting. |

For safe diagnostics, print only non-secret identifiers:

```bash
printf 'tenant=%s parent=%s invocation=%s\n' \
  "$C2J_CURRENT_TENANT_ID" \
  "$C2J_CURRENT_JOB_ID" \
  "$C2J_CURRENT_INVOCATION_HASH"
```

For Go integrations, the implemented helpers are in
[`pkg/jobcontext`](pkg/jobcontext/context.go),
[`pkg/childbroker`](pkg/childbroker/broker.go), and
[`pkg/recipejob`](pkg/recipejob/list.go). Parsing parent environment and attaching
metadata is not a substitute for broker/lease-scoped submission.
