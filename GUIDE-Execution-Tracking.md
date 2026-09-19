# Execution tracking user guide

This guide covers the currently available c2j commands for inspecting job state,
following recipe progress, understanding handoffs, and finding child jobs. It
uses the typed-route JobDB API.

Execution-environment tracking is not yet enabled: the CLI does not currently
record allocated CPU, memory, platform, or image identities, enforce recipe
resource requirements, or filter jobs by environment compatibility. Those
features remain in the
[execution-requirements design](C2J_PORTABLE_EXECUTION_REQUIREMENTS_DESIGN.md).
The existing allocation parser is a library component, not an available CLI
feature. The remaining publication dependency is documented in
[this JobDB follow-up request](JOBDB_CLIENT_PAYLOAD_PUBLICATION_REQUEST.md);
the overall feature is not complete.

## Choose the runtime and job

Use the same runtime and tenant for submission, inspection, and execution.
Examples below use the local embedded runtime, selected with `--embed`. For a
remote runtime, replace that option with:

```bash
--jobdb https://jobdb.example.com/my-tenant
```

Run cell-scoped commands from your configured repository, or select a cell
explicitly with `--cell`. Job identity consists of both tenant ID and job ID;
the embedded runtime uses tenant `0`.

Submit without executing, then copy the returned `job_id`:

```bash
c2j submit --recipe-file ./recipes/my-recipe.yaml --json --embed
c2j list --self --job-id JOB_ID --json --embed
```

Replace `JOB_ID` with that ID in subsequent examples. Submission `--json` and
`--run` cannot be combined.

The current JobDB version requires fresh format-3 storage and matching runtime
versions. There is no in-place migration of old jobs or execution history. See
[deployment notes](JOBDB_UPGRADE_NOTES.md) before changing an existing installation.

## Inspect without executing

`list` reads job state; it does not claim or run a job.

```bash
c2j list --self --embed
c2j list --cell github.com/acme/app --json --embed
c2j list --self --job-id JOB_ID --json --embed
```

The table includes identity, status, storage location, job type, creation time,
availability time, and `NEXT`. `NEXT` shows a typed route as JSON when present;
otherwise it shows dependency job IDs when available. Use JSON to inspect both
routing and dependencies together.

Listings are scoped to the selected cell as well as the tenant. Passing
`--job-id` does not bypass that cell filter.

### Status and history

By default, lists include `READY`, `EXPIRED`, `PENDING_JOBS`, `AWAITING_FUTURE`,
`ACTIVE`, and `CRASH_CONCERN`. Completed and cancelled jobs require an explicit
status filter:

```bash
c2j list --self --status completed --status cancelled --all --embed
c2j list --self --status active --status pending_jobs --embed
```

Status filter values are case-insensitive. These are scheduler states, not
recipe-step outcomes:

| Status | What to investigate |
| --- | --- |
| `READY` | Work is available for its route; this does not prove your worker can handle it. |
| `ACTIVE` | A worker holds a live execution lease. |
| `CRASH_CONCERN` | A lease expired without finalization; check the worker and recovery state. |
| `PENDING_JOBS` | Dependencies are outstanding; inspect `wait_for`. |
| `AWAITING_FUTURE` | Work is deferred; inspect `available_at`. |
| `EXPIRED` | The runtime reports the job as expired; inspect its expiry information. |
| `COMPLETED` | Execution has been finalized; this alone does not establish recipe success. |
| `CANCELLED` | The runtime reports cancellation. |

There is no `--status failed` filter. Use recipe execution history/outcomes to
distinguish success from failure. A pending human-input task can be `READY`
for that task route; it does not have a separate human-input status.

`--all` fetches all matching pages, not all statuses. Without it, inspect
`next_page_token` in JSON or the token printed below the table. Pass the token
back with the same filters:

```bash
c2j list --self --page-size 50 --json --embed
c2j list --self --page-size 50 --page-token TOKEN --json --embed
```

Creation-time filters accept RFC3339 timestamps, for example
`--created-after 2026-09-19T00:00:00Z`. They filter creation time, not the time
of the latest execution attempt.

### Understand routing and handoffs

A route has independent `jobType` and `taskType` fields:

```json
{"jobType":"recipe","taskType":"input:collect_user_input"}
```

This routes work to the named input task. A route with only
`{"jobType":"recipe"}` selects recipe job work. Routes describe what work is
needed, not which machine or resource allocation should execute it.

Job and task identifiers are case-sensitive and may contain colons, commas, or
spaces. Do not split, trim, or combine them into a delimiter-based identifier.
Filter pending tasks by supplying a complete JSON route:

```bash
c2j list --self \
  --waiting-for '{"jobType":"recipe","taskType":"input:collect_user_input"}' \
  --json --all --embed
```

Repeat `--waiting-for` for additional task routes. Both fields are required for
this filter. To filter by job type instead, use `--job-type recipe`; repeat the
flag for additional types. Neither flag accepts comma-separated lists, and the
old `--waiting-for JOBTYPE:TASKTYPE` syntax is unsupported.

### Read the JSON fields

`list --json` returns an object with a `jobs` array and an optional
`next_page_token`. Useful fields on each job include:

| Field | Meaning |
| --- | --- |
| `tenant_id`, `job_id` | Durable job identity. |
| `status`, `store` | Current scheduler state and active/archived storage. |
| `next_route` | Requested job/task work, with `jobType` and optional `taskType`. |
| `task_wait` | Pending task coordinates: `inputOrdinal`, `outputOrdinal`, `inputHash`, and `resumeJobType`. |
| `wait_for` | Dependency job IDs. |
| `available_at` | Scheduling availability time, not a promise of execution start. |
| `lease_expires_at` | Current lease expiry, when supplied. |
| `client_payload`, `client_payload_revision` | Client-owned JSON state and its revision. |

Optional fields may be absent. Do not infer a pending task's output ordinal from
its input ordinal: use the supplied coordinates. `resumeJobType` identifies the
job work to resume after task completion; it is not the currently pending task.

Client payload is separate from metadata, recipe input, and JobDB scheduling
state. Its revision tracks payload updates, not execution attempts or percentage
complete. Payload keys have client-defined meaning; JSON object order is not
significant. Ordinary task completion preserves it unless an explicit update
is supplied. There is no general-purpose CLI payload-edit command.

For example, with `jq` installed:

```bash
c2j list --self --json --all --embed |
  jq '.jobs[] | {job_id, status, next_route, wait_for, client_payload_revision}'
```

## Follow execution progress

`run` executes or continues a job. It is not a read-only watch command, even
when used with a fail-on-blocking policy.

```bash
c2j run --job-id JOB_ID --embed
```

`c2j run one` is the explicit equivalent. The runner first reconstructs available
cached history, then attempts execution. Interactive terminals show an updating
tree; redirected output and CI use line-oriented progress.

The story identifies recipes, sequences, operations, steps, context patches,
states, and transitions. Completed subtrees can be collapsed in the terminal
view. Retry/attempt annotations appear when available.

- `cached` / `[cached]` means reconstructed recorded history, not newly executed
  work.
- `live` / `[live]` means progress observed during this invocation. A resumed
  invocation can still reuse recorded task results.
- `done`, `failed`, `canceled`, and `skipped` describe story-node outcomes.
- `waiting: ...` explains an external blocker using scheduler status,
  `next_route`, `missing_route`, or dependency IDs when available.

History reconstruction can be partial or unavailable; the runner reports
warnings and may still continue execution. These displays are not a resource
usage meter or a standalone, read-only history export.

### Wait, fail promptly, or handle input

```bash
c2j run --job-id JOB_ID --wait-timeout 30m --poll-interval 5s --embed
c2j run --job-id JOB_ID --on-not-ready fail --input-mode fail --embed
c2j run --job-id JOB_ID --ci --embed
```

The default wait timeout is 15 minutes and the polling interval is 5 seconds.
The wait timeout controls waiting on external blocking work, not a hard deadline
for all recipe execution.

`--on-not-ready` accepts `wait`, `fail`, `fail-on-lease`,
`fail-on-pending-jobs`, `fail-on-future`, and `fail-on-missing-capability`.
The last option checks a missing typed route; despite its name, it does not
compare CPU, memory, or image requirements.

For pending human input, `--input-mode prompt` collects and submits a response,
`ops` emits an `input_required` JSON event, and `fail` exits without prompting.
Interactive terminals default to prompting; non-terminal and CI runs default to
`ops`. `--ci` requests machine-readable input handling and line-oriented progress.
The JSON event includes `kind`, `tenant_id`, `job_id`, `form`, and `blocking`.

Run stdout can also contain story and waiting lines: `--ci` does not turn the
entire stream into JSON. Use `list --json` for a structured status snapshot.

For `run` / `run one`, exit codes are `0` for success, `1` for execution/general
failure, `2` for wait timeout, `3` for required input, `4` for a not-ready policy
failure, and `5` for run-option/identity validation failure. These meanings
should not be generalized to every c2j command.

## Track child jobs

From inside a recipe operation, the injected parent context scopes this command
to children started by the current operation invocation:

```bash
c2j list children --json --embed
```

Outside that operation, supply parent identity and either an invocation hash or
`--all-ops`:

```bash
c2j list children --parent-tenant-id 0 --parent-job-id PARENT_JOB_ID \
  --all-ops --json --all --embed
```

The parent tenant must match the selected runtime tenant. Child listings include
recipe and parent-lineage information, as well as routing and client-payload
state. Their default statuses also exclude completed/cancelled jobs; add the
appropriate `--status` filters to inspect finished children. `--all-ops` widens
the parent invocation scope; `--all` fetches pages.

## Track worker handoffs

For external schedulers using a remote runtime:

```bash
c2j ready --jobdb https://jobdb.example.com/my-tenant
c2j run any --jobdb https://jobdb.example.com/my-tenant
```

`ready` is a read-only count of ready recipe jobs across the selected tenant,
not a reservation or a cell-scoped list. `run any` claims one available work
item and executes it. Another worker can consume work between those commands.

`run any` reports completion or rescheduling. A handoff is reported with
`status=rescheduled` and a separate JSON `next_route`; that means the current
lease was handed back, not that the whole recipe finished. It prints
`no jobs found` and exits successfully when no work can be claimed. Use job
state and execution outcomes, not just that process's exit code, to track the
overall recipe result.

For ongoing processing, `c2j run loop --jobdb URL --concurrency 4` starts a
non-interactive tenant worker. It requires a remote runtime. Use listings for
inspection and targeted `run` or an input-handling service for human input.

## When something looks wrong

- **A job disappeared:** check the cell and tenant, then explicitly list
  completed/cancelled statuses. Check pagination too; `--all` does not broaden
  statuses.
- **A ready job does not progress:** inspect `next_route`. It may need a
  different task worker or human input rather than ordinary recipe execution.
- **A job appears stuck:** inspect `wait_for`, `available_at`, and lease expiry
  before attempting another execution.
- **An execution flag is unknown:** `--execution-*` allocation inputs and
  `--compatible-with-execution` are not registered on commands yet. Setting the
  corresponding environment variables does not enable resource enforcement.
- **An old database is rejected:** follow the format-3 deployment requirements;
  this release does not translate old routes or migrate stored jobs.

For embedding these inspection features in another application, see the
[recipe-job API guide](GUIDE-Cortex-RecipeJob-API.md). For the exact upstream
route contract, see [the typed-route migration guide](MIGRATION-TYPED-ROUTES.md).
