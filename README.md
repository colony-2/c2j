# c2j

`c2j` is the local job-oriented CLI for submitting and running recipe jobs through JobDB.

Use it when you want to:

- submit a recipe job from a named recipe or a local recipe file
- run or continue an existing job
- count and claim available jobs for external scheduling
- run a tenant worker loop with bounded local concurrency
- use the embedded local runtime for fast iteration
- inspect the current cell configuration used for job targeting
- list jobs for a cell

Examples below assume you are running from the repo root.

For execution requirements, actual allocation inputs, compatibility filtering,
job status, progress, handoffs, and child-job lineage, see the
[execution tracking user guide](GUIDE-Execution-Tracking.md).

Recipes can declare an `execution` block for CPU, memory, scratch, platform,
and image. Node-level `execution_needs` adds templatable, scoped overrides that
inherit within a job and restore the parent on exit. Completed tasks replay
without requesting their historical environments. Pass actual executor facts using individual `--execution-*` flags
or `C2J_EXECUTION_*` variables. If requirements change or the environment is
insufficient, execution yields the same job and reports `environment_required`.
A provisioner can then resume it in a compatible environment; c2j does not
provision resources itself. Listings show submission/latest-yield requirements
only for waiting jobs; in-flight needs are not presented as current snapshots.

## JobDB upgrade compatibility

The typed-route JobDB update requires fresh format-3 databases/artifact storage
and matching server/worker versions; existing jobs are not migrated. Job and
task types now travel separately, preserving existing task names. Read
[the upgrade notes](JOBDB_UPGRADE_NOTES.md) before deploying it. No existing data
is automatically reset.

## Command Summary

```bash
c2j self
c2j cells
c2j init
c2j version
c2j submit
c2j run
c2j run one
c2j run any
c2j run loop
c2j ready
c2j list
c2j test
```

Use `go run ./cmd/c2j --help` or `c2j --help` to see the full command tree.

## Version Information

Use the `version` subcommand to identify the exact executable you are running:

```bash
c2j version
```

Release builds report the release version injected by the release pipeline. Local
`go build`, `go run`, and `go install` builds fall back to Go's embedded VCS
metadata, so untagged builds include the current git revision when it is
available. Dirty suffixes are only included when Go reports that the worktree was
modified at build time.

## Quick Start

### 1. Check current-cell resolution

`submit` targets the current cell by default. That usually comes from `.c2j/config.yaml`, but supported project types can also be auto-detected.

Inspect the resolved config:

```bash
c2j self
```

List allowed dependent cells:

```bash
c2j cells
```

Generate a starter config if needed:

```bash
c2j init --stdout
```

If the current directory does not resolve as a cell, either:

- create `.c2j/config.yaml`, or
- pass `--cell <repo-or-path>` explicitly to `submit` or `list`

### 2. Submit and run a build or evolve job

Build is the default. Add `--evolve` to select the evolve recipe:

```bash
c2j submit "Implement the new endpoint" --run --embed
c2j submit "Improve retry behavior" --evolve --run --embed
```

That does all of the following:

- starts an embedded JobDB runtime
- submits the job
- resolves the named recipe in the target cell, falling back to the shared recipe if absent
- immediately executes it against the target cell

See [submitting jobs](#submitting-jobs) for the hosted recipe names and [advanced recipe selection](#advanced-recipe-selection) for custom local files.

### 3. Continue or inspect a job later

Find recent jobs for the current cell:

```bash
c2j list --self --embed
```

Continue a submitted job:

```bash
c2j run --job-id <job-id> --embed
```

## Current Cell Commands

### `c2j self`

Shows how `c2j` resolves the current cell from `.c2j/config.yaml` or supported auto-detection.

```bash
c2j self
c2j self --json
```

Fields include:

- `short_name`
- `repo`
- `ref`
- `root_repo`
- `root_ref`
- `pattern`

### `c2j cells`

Lists dependent cells allowed by the current config.

```bash
c2j cells
c2j cells --json
```

This is mainly useful when you want to target another cell by short name and need to verify how config expands it.

### `c2j init`

Writes a commented `.c2j/config.yaml` template and installs bundled c2j skills for Codex-compatible agents.

```bash
c2j init
c2j init --stdout
c2j init --force
c2j init --no-skills
c2j init --with-op-skills
```

The generated template can derive values from the Go module in the current repo when `base: go` is appropriate. `--stdout` prints only the config and does not install skills.

Bundled skill install controls:

```bash
c2j init --skills-scope auto
c2j init --skills-scope project
c2j init --skills-scope user
c2j init --skills-force
```

`auto` currently installs to the project-local `.agents/skills` directory. `user` installs to `${CODEX_HOME:-$HOME/.codex}/skills`. Install metadata is recorded in `.c2j/skills-lock.yaml`.

You can also install the repo skills directly with the public `skills` tool:

```bash
npx skills add colony-2/c2j
```

## Submitting Jobs

### Basic forms

Submit a build job (the default), or choose evolve:

```bash
c2j submit "Implement the new endpoint" --embed
c2j submit "Implement the new endpoint" --build --embed
c2j submit "Improve the retry behavior" --evolve --embed
```

These submit the names `build` and `evolve` through the existing recipe resolution system. At execution time, c2j first looks for `.c2j/recipes/build.yaml` or `.c2j/recipes/evolve.yaml` in the **target cell's repository**, at its configured ref. This also applies to `--cell`; the submitting project's recipes do not override another target cell's recipes. As with other named recipes, uncommitted files are not used.

If that recipe file is absent, c2j resolves one of these shared recipes instead:

- `git+https://github.com/colony-2/recipes.git//build.yaml@main`
- `git+https://github.com/colony-2/recipes.git//evolve.yaml@main`

The shared repository must provide `build.yaml` and `evolve.yaml` at its root on `main`. These recipes are not bundled with c2j; until published, jobs needing the fallback cannot execute. Missing refs, authentication/network failures, and invalid recipes remain errors rather than triggering fallback. Resolution is pinned and cached by the existing job execution machinery. A shared recipe still operates on the target cell's worktree.

Every submission requires a non-empty prompt, passed as `inputs.prompt`. Omit the argument in a terminal to enter it interactively:

```bash
c2j submit --evolve --embed
```

For automation, provide the positional prompt or a string `prompt` in `--inputs-json`/`--inputs-file`; missing prompts with non-terminal stdin fail immediately. Interactive prompts go to stderr, so `--json` stdout stays machine-readable.

### Build/evolve input contract

c2j automatically includes the selected type alongside the prompt:

```json
{"prompt": "Implement the new endpoint", "type": "build"}
```

```json
{"prompt": "Improve retry behavior", "type": "evolve"}
```

`type` is derived from the selected recipe, not used to select it: use `--evolve` to submit an evolve job. A matching `type` in `--inputs-json` or `--inputs-file` is accepted; a conflicting or non-string value is rejected before submission. The same contract applies to target-cell recipes and shared fallbacks, including explicit selection of the names `build` and `evolve`.

Both conventional recipes must declare these inputs and bind them for recipe logic:

```yaml
input_schema:
  prompt:
    type: string
    required: true
  type:
    type: string
    required: true
inputs:
  prompt: "{{ inputs.prompt }}"
  type: "{{ inputs.type }}"
```

Custom recipe names, explicit Git selectors, and local recipe files do not receive an automatic `type`; their own input contracts apply and any supplied `type` is preserved.

### Advanced recipe selection

For a custom recipe, explicitly opt into advanced selection:

```bash
c2j submit "Review the changes" --advanced-recipe review --embed
c2j submit "Review the changes" --advanced-recipe-file ./recipes/review.yaml --embed
```

Submit and run immediately:

```bash
c2j submit "Review the changes" --advanced-recipe-file ./recipes/review.yaml --run --embed
```

`--advanced-recipe` accepts a target-cell recipe name or explicit git selector. `--advanced-recipe-file` embeds a local YAML file, including uncommitted changes. These flags are mutually exclusive with each other and with `--build`/`--evolve`. The old `--recipe`/`--recipe-file` flags remain hidden, deprecated aliases on `submit`; `c2j test` retains its existing flag names. Custom names and explicit selectors do not get a hosted fallback; the conventional names `build` and `evolve` do.

Custom recipes submitted through the CLI must also declare the prompt in their input schema:

```yaml
input_schema:
  prompt:
    type: string
    required: true
```

### Passing inputs

Inline JSON:

```bash
c2j submit "Run the requested task" \
  --advanced-recipe-file ./recipes/my-recipe.yaml \
  --inputs-json '{"message":"hello"}' \
  --run \
  --embed
```

Inputs file in JSON or YAML:

```bash
c2j submit "Run the requested task" \
  --advanced-recipe-file ./recipes/my-recipe.yaml \
  --inputs-file ./recipes/test-inputs.yaml \
  --run \
  --embed
```

Positional prompt shortcut:

```bash
c2j submit "Summarize the repo" --advanced-recipe my-prompt-recipe --embed
```

The positional argument is merged as `inputs.prompt`.

Rules:

- `--inputs-json` and `--inputs-file` are mutually exclusive
- the positional prompt cannot also be provided as `inputs.prompt`

### Attaching files

Attach local files as job artifacts with repeatable `--artifact` flags:

```bash
c2j submit "Run the requested task" \
  --advanced-recipe-file ./recipes/review-docs.yaml \
  --artifact ./docs/brief.md \
  --artifact requirements=./docs/requirements.md \
  --run \
  --embed
```

`--artifact <path>` uses the file basename as the artifact name.
`--artifact <name>=<path>` sets an explicit artifact name.

Recipes bind submitted artifacts into an op inbox explicitly:

```yaml
sequence:
  - id: inspect
    op: command_execution
    artifacts:
      brief.md: '${{ context.artifacts["brief.md"] }}'
    inputs:
      run: 'cat "${{ context.environment.op.inbox }}/brief.md"'
```

### Choosing the target cell

Use the current cell:

```bash
c2j submit "Implement the new endpoint" --self --embed
```

Use another cell explicitly:

```bash
c2j submit "Improve retry behavior" --evolve \
  --cell github.com/colony-2/root \
  --embed
```

`--cell` accepts:

- a canonical repo string
- a clone URL
- a local repository path
- a configured short name when `.c2j/config.yaml` defines a pattern

Rules:

- `--self` and `--cell` are mutually exclusive
- if no `--cell` is given, `c2j` behaves as if you targeted `--self`

### Getting machine-readable output

If you only want the submitted job identity:

```bash
c2j submit "Implement the new endpoint" \
  --json \
  --embed
```

This emits:

```json
{
  "tenant_id": "0",
  "job_id": "job-...",
  "recipe": "build"
}
```

Note:

- `--json` and `--run` are mutually exclusive

## Node workspaces

Use `workspace: {cell: cellB}` on a recipe node to run against another cell's data. Every explicit declaration starts a fresh workspace; descendants share its durable Git snapshots. Cell and optional `ref` values accept runtime templates. When the node exits, execution resumes in the enclosing workspace without adopting its changes or publishing to the target cell.

```yaml
sequence:
  - id: inspect
    workspace: {cell: cellB, ref: main}
    op: command_execution
    inputs:
      working_directory: "{{ context.environment.op.worktree_path }}"
      run: "cat README.md"
```

`context.workflow.cell` remains the owning job's cell; `context.workspace.cell` and `context.git.*` describe the active workspace. See [Run a recipe node in another cell](GUIDE-Node-Workspaces.md) for scope, replay, child-job, and deployment details.

## Testing Recipes

`c2j test` compiles, validates, and runs recipe test suites locally. It does not call the old Colony2 API.

Compile a suite to canonical IR:

```bash
c2j test compile \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./recipes/my-recipe.test.yaml \
  --out ./tmp/compiled-test.json
```

Run a local suite:

```bash
c2j test run \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./recipes/my-recipe.test.yaml \
  --artifact-mode inline
```

Run one case:

```bash
c2j test case run \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./recipes/my-recipe.test.yaml \
  --case-id smoke
```

Useful flags:

- `--recipe <name-or-git-selector>` targets a current-cell recipe or explicit git selector
- `--recipe-file <path>` uses a local inline recipe file
- `--case <id>` filters suite mode to selected cases
- `--parallelism <n>` controls local case concurrency
- `--out-dir <dir>` defaults to `.c2j/test-results/<timestamp>/`
- passthrough cases use a disposable embedded runtime automatically

## Running Jobs

`c2j run` executes or continues one existing job and prints live story progress
to stdout. `c2j run one` is the explicit form of the same command.

Basic usage:

```bash
c2j run --job-id <job-id> --embed
c2j run one --job-id <job-id> --embed
```

Common variants:

```bash
c2j run --job-id <job-id> --wait-timeout 30m --embed
c2j run --job-id <job-id> --input-mode fail --embed
c2j run --job-id <job-id> --ci --embed
```

Important behavior:

- completed jobs return successfully
- failed jobs return a non-zero exit code
- suspended jobs may wait, prompt, or fail depending on flags
- when input is pending, interactive terminals default to prompting
- in CI or non-terminal mode, input handling defaults to `ops`

### Input handling

`--input-mode` controls what happens when a job is blocked on user input:

- `prompt`
  Prompt on stdin/stdout and submit the response
- `ops`
  Emit machine-readable `input_required` JSON and exit non-zero
- `fail`
  Exit immediately when input is required

`--ci` enables machine-readable input-required behavior without prompting.

### Not-ready handling

`--on-not-ready` controls how `run` reacts when a job is not runnable yet:

- `wait`
- `fail`
- `fail-on-lease`
- `fail-on-pending-jobs`
- `fail-on-future`
- `fail-on-missing-capability`

With the default `wait`, `run` will print `waiting: ...` lines and poll until
the job becomes runnable or the wait timeout is reached.

### Exit codes

`c2j run` uses distinct exit codes:

- `1`: general failure or job failure
- `2`: wait timeout
- `3`: input required
- `4`: job not runnable under the selected policy
- `5`: invalid job identity or invalid run arguments

## Worker Modes

`c2j run loop` runs a long-lived worker for one tenant. It leases available jobs
from a remote JobDB runtime and executes up to the configured local concurrency.

Basic usage:

```bash
c2j run loop --jobdb http://localhost:9047/<tenant-id> --concurrency 4
```

Important behavior:

- `--concurrency <n>` controls how many jobs this process can run at once
- `--jobdb` must be a remote `http://host/tenant` or `https://host/tenant` URI
- `--embed` is not available for `run loop`
- `--jobdb embed:///` is rejected for `run loop`
- `run loop` is non-interactive; use `run` or an ops surface for jobs that need input

### Loose scheduling with `ready` and `run any`

`c2j ready` prints the number of currently ready recipe jobs for one tenant:

```bash
c2j ready --jobdb http://localhost:9047/<tenant-id>
```

`c2j run any` atomically polls for one available item of recipe work, leases it,
runs it, and exits. If no lease is available, it exits successfully after
printing `no jobs found`.

```bash
c2j run any --jobdb http://localhost:9047/<tenant-id>
```

These can be composed by an external scheduler:

```bash
count=$(c2j ready --jobdb http://localhost:9047/<tenant-id>)
if [ "$count" -gt 0 ]; then
  for _ in $(seq 1 "$count"); do
    c2j run any --jobdb http://localhost:9047/<tenant-id> &
  done
  wait
fi
```

`ready` is only a non-mutating snapshot and can become stale under competing
workers. `run any` uses JobDB polling so finding available work and acquiring the
lease happen in one runtime operation.

## Listing Jobs

Go applications can list remote jobs without invoking the CLI using the
supported [`pkg/joblist` API](pkg/joblist/README.md). See the
[standalone example](examples/listjobs/main.go) and
[implementation response](C2J_FEATURE_REQUESTS_RESPONSE.md).

List jobs for the current cell:

```bash
c2j list --self --embed
```

List as JSON:

```bash
c2j list --self --json --embed
```

Filter by status:

```bash
c2j list --self --status pending_jobs --status active --embed
```

List jobs for another cell:

```bash
c2j list --cell github.com/colony-2/root --embed
```

Useful filters:

- `--job-id`
- `--job-type`
- `--status`
- `--waiting-for`
- `--created-after`
- `--created-before`
- `--page-size`
- `--page-token`
- `--all`

Filter by a pending task using a JSON route:

```bash
c2j list --self --waiting-for '{"jobType":"recipe","taskType":"input:collect_user_input"}' --embed
```

Repeat `--waiting-for` for multiple routes and `--job-type` for multiple job
types. These flags accept literal identifiers, not comma-separated lists;
`--waiting-for JOBTYPE:TASKTYPE` is no longer supported because either type may
contain colons. Identifiers are case-sensitive and are not trimmed or encoded.
List JSON exposes `next_route` (`jobType`, optional `taskType`) and `task_wait`
instead of the old `next_need` and flat `task_wait_*` fields. The human-readable
`NEXT` column also shows the route as JSON.

## Parent and child jobs

Commands and extensions can run nested `c2j submit` calls that automatically
record parentage through the current job's child-submission broker. See the
[parent and child jobs guide](C2J_CHILD_JOBS_GUIDE.md) for setup, complete recipe
examples, child listing, result handling, environment variables, and limits.

## Embedded Runtime

`--embed` is shorthand for:

```bash
--jobdb embed:///
```

Use it when you want a local self-contained runtime instead of a remote JobDB server.

Behavior:

- starts embedded Postgres and Strata as needed
- uses a persistent runtime root on disk
- always uses tenant `0`
- works well for local recipe authoring and debugging

Defaults:

- runtime URL: `embed:///`
- runtime root: `~/.c2j/embed/default`

Notes:

- only one `c2j` process can own a given embedded runtime root at a time

## JobDB Configuration

`c2j` reads these environment variables:

- `C2J_JOBDB`

JobDB URI forms:

- `https://jobdb.example.com/<tenant-id>`
- `http://localhost:9047/<tenant-id>`
- `embed:///`

Examples:

```bash
export C2J_JOBDB=http://localhost:9047/my-tenant
```

`--jobdb` overrides `C2J_JOBDB` for that command. Project config may also define:

```yaml
jobdb: https://jobdb.example.com/my-tenant
```

For local embedded mode in project config:

```yaml
jobdb: embed:///
```

## Common Workflows

### Local recipe authoring loop

```bash
c2j self
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/my-recipe.yaml --run --embed
```

### Detached submit, then later run

```bash
c2j submit "Implement the new endpoint" --json --embed
c2j run --job-id <job-id> --embed
```

### Run against a remote runtime instead of embed

```bash
c2j submit "Run the requested task" \
  --advanced-recipe-file ./recipes/my-recipe.yaml \
  --jobdb http://localhost:9047/my-tenant \
  --run
```

### Target another cell explicitly

```bash
c2j submit "Run the requested task" \
  --advanced-recipe-file ./recipes/my-recipe.yaml \
  --cell github.com/colony-2/root \
  --run \
  --embed
```

## Gotchas

- `--build`, `--evolve`, `--advanced-recipe`, and `--advanced-recipe-file` are mutually exclusive on `submit`
- `--json` and `--run` are mutually exclusive on `submit`
- `--inputs-json` and `--inputs-file` are mutually exclusive
- `--self` and `--cell` are mutually exclusive
- `--artifact` names must be unique relative paths; directories are not supported yet
- `self`, `cells`, and implicit current-cell submission depend on config or supported auto-detection succeeding
- short cell names require a config pattern; without config, use an explicit repo or path
- `submit --advanced-recipe-file` is clearer than passing a local file path through `--advanced-recipe`

## Related Files

- command entrypoint: [main.go](main.go)
- embedded runtime notes: [embed-swf-mode-spec.md](embed-swf-mode-spec.md)
- recipe authoring docs: [RECIPE_AUTHORING_GUIDE.md](../../recipes/guides/RECIPE_AUTHORING_GUIDE.md)
