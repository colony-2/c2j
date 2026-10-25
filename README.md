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

For log levels and selective DEBUG output, see the [logging user guide](GUIDE-Logging.md).
For shared logger configuration across C2J, JobDB, Pulse, and Cortex, see the
[logging integration guide](GUIDE-Logging-Integration.md).

For resolving agent-returned paths against published artifact names, see the
[`path_match` user guide](GUIDE-Path-Matching.md).

For selecting restart points and inspecting context before a patch, see the
[restart context API guide](GUIDE-Restart-Context.md).

For execution requirements, actual allocation inputs, compatibility filtering,
job status, progress, handoffs, and child-job lineage, see the
[execution tracking user guide](GUIDE-Execution-Tracking.md).

For schema-driven human input and document review using existing artifact
attachments, see the [recipe author's guide](GUIDE-Review-Recipe-Authors.md)
and [application integration guide](GUIDE-Structured-Input-And-Review.md).
Reviews use one `input` op with `form.kind: review`, ordinary questions, and
stored document references. No review extension or preparation recipe is needed.

Recipes can declare an `execution` block for CPU, memory, scratch, platform,
and image. Node-level `execution_needs` adds templatable, scoped overrides that
inherit within a job and restore the parent on exit. Completed tasks replay
without requesting their historical environments. Pass actual executor facts using individual `--execution-*` flags
or `C2J_EXECUTION_*` variables. If requirements change or the environment is
insufficient, execution yields the same job and reports `environment_required`.
A provisioner can then resume it in a compatible environment; c2j does not
provision resources itself. Listings show submission/latest-yield requirements
only for waiting jobs; in-flight needs are not presented as current snapshots.

Commands and extensions execute directly in the worker environment. Provision
any container isolation externally. Operation-level Shai execution has been
removed; see the [breaking release notes and migration](RELEASE-NOTES-Remove-Op-Sandboxing.md).

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
c2j run with-lease
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

See [Execution tools](EXECUTION_TOOLS.md) for lazy pnpm, Nix, and uv tool dependencies and provider-owned cache storage.
See [Extension ops](EXTENSION_OPS.md) for authoring, publishing, and invoking prebuilt Nix ops.

## Nix package

The Nix package contains only the `c2j` executable, with no Node/npm wrapper or
bundled tool runtimes. The package definition supports Linux and macOS on x86-64
and ARM64 for local builds. CI does not build or publish the Nix package.

```bash
nix run github:colony-2/c2j/main#c2j -- version
# Install into your profile:
nix profile add github:colony-2/c2j/main#c2j
```

Pin a commit instead of `main` for reproducible builds. New revisions may require
local compilation because there is no automatic Cachix publication. `main` builds
report `unstable-<commit>` as their version. Runtime tools such as Git, Nix, pnpm,
and uv are supplied separately by the execution environment when needed.

Maintainers: `nix build .#c2j` builds the executable; `nix flake check` also runs
its installation checks. After changing Go dependencies, update `vendorHash` in
`nix/package.nix` to the hash reported by a build with `lib.fakeHash`.

## Quick Start

### 1. Check current-cell resolution

`submit` targets the current cell by default. That usually comes from `.c2/config.yaml`, but supported project types can also be auto-detected.

Configuration now lives in `.c2/config.yaml`; the old `.c2j` directory is not
searched or migrated automatically. Move existing configuration and recipes to
`.c2` before running this version.

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

- create `.c2/config.yaml`, or
- pass `--cell <repo-or-path>` explicitly to `submit` or `list`

### 2. Submit and run a build job

Submissions use the build recipe:

```bash
c2j submit "Implement the new endpoint" --run --embed
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

Shows how `c2j` resolves the current cell from `.c2/config.yaml` or supported auto-detection.

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

The table includes a description column. JSON rows contain `short_name`, `repo`,
and an optional `description`; multiline descriptions retain their newlines in JSON.

### `c2j cell add`

Add or update a manually listed dependent cell in the nearest `.c2/config.yaml`:

```bash
c2j cell add github.com/acme/auth --description 'Authentication service'
c2j cell add auth --description 'Identity and sessions' --stdout
c2j cell add auth --description ''
```

Short names require a configured or derived pattern. `--stdout` previews the
complete YAML without writing. Omitting `--description` preserves the current
description; an empty value clears it. Repeating the same edit leaves the file
unchanged. Edits preserve comments and unrelated config; YAML indentation may
be normalized. If no config exists, run `c2j init --no-skills` at the project root.

Manual lists support simple strings and detailed mappings, including mixed lists:

```yaml
pattern: 'github.com/acme/${{ cell }}'
dependents:
  - github.com/acme/shared
  - repo: github.com/acme/auth
    description: Authentication service
```

A manual list defines the complete allowed set and bypasses discovery filtering.
Automatically detected cells are used directly and stay live. Adding an already
detected cell without a description succeeds without writing. Adding undetected
cells or descriptions in discovery mode is not supported by this command;
descriptions currently belong to manual entries.

### `c2j init`

Writes a commented `.c2/config.yaml` template and installs bundled c2j skills for Codex-compatible agents.

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

`auto` currently installs to the project-local `.agents/skills` directory. `user` installs to `${CODEX_HOME:-$HOME/.codex}/skills`. Install metadata is recorded in `.c2/skills-lock.yaml`.

Refresh the installed skills independently of configuration:

```bash
c2j skills update
c2j skills update --scope user
c2j skills update --force  # replace locally edited bundled skill folders
```

Run from the project root. This installs the bundle shipped in your c2j binary;
upgrade c2j first for newer references. It preserves local edits by default and
reports skipped skills. It does not fetch live GitHub skills or install c2ops
executables. The c2ops catalog records the upstream revision it was reviewed
against. Reload the agent session after updating. See the
[skill CLI reference](skills/c2j-operations/references/cli-reference.md).

You can also install the repo skills directly with the public `skills` tool:

```bash
npx skills add colony-2/c2j
```

## Submitting Jobs

### Basic forms

Submit a build job:

```bash
c2j submit "Implement the new endpoint" --embed
```

Submissions use the name `build` through the existing recipe resolution system. At execution time, c2j first looks for `.c2/recipes/build.yaml` in the **target cell's repository**, at its configured ref. This also applies to `--cell`; the submitting project's recipes do not override another target cell's recipes. As with other named recipes, uncommitted files are not used.

If that recipe file is absent, c2j resolves the shared recipe instead:

- `git+https://github.com/colony-2/recipes.git//build.yaml@main`

The shared repository must provide `build.yaml` at its root on `main`. This recipe is not bundled with c2j; until published, jobs needing the fallback cannot execute. Missing refs, authentication/network failures, and invalid recipes remain errors rather than triggering fallback. Resolution is pinned and cached by the existing job execution machinery. A shared recipe still operates on the target cell's worktree.

Every submission requires a non-empty prompt, passed as `inputs.prompt`. Omit the argument in a terminal to enter it interactively:

```bash
c2j submit --embed
```

For automation, provide the positional prompt or a string `prompt` in `--inputs-json`/`--inputs-file`; missing prompts with non-terminal stdin fail immediately. Interactive prompts go to stderr, so `--json` stdout stays machine-readable.

### Build input contract

Build submissions include `type: build` alongside the prompt:

```json
{"prompt": "Implement the new endpoint", "type": "build"}
```

`type` is fixed to `"build"` for build submissions; it does not select a recipe. A matching `type` in `--inputs-json` or `--inputs-file` is accepted; a conflicting or non-string value is rejected before submission. The same contract applies to target-cell recipes and shared fallbacks, including explicit selection of the name `build`.

The build recipe must declare these inputs and bind them for recipe logic:

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

`--advanced-recipe` accepts a target-cell recipe name or explicit git selector. `--advanced-recipe-file` embeds a local YAML file, including uncommitted changes. These flags are mutually exclusive. The old `--recipe`/`--recipe-file` flags remain hidden, deprecated aliases on `submit`; `c2j test` retains its existing flag names. Custom names and explicit selectors do not get a hosted fallback; only the name `build` does.

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

When provided as nonblank text, `inputs.prompt` is also stored verbatim in job
metadata as `prompt` and exposed in job and recipe listing JSON. Empty values,
non-string values, and secret references are omitted from this metadata field.
This records the submitted prompt; it does not evaluate recipe defaults or templates.

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
c2j submit "Improve retry behavior" \
  --cell github.com/colony-2/root \
  --embed
```

`--cell` accepts:

- a canonical repo string
- a clone URL
- a local repository path
- a configured short name when `.c2/config.yaml` defines a pattern

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
- `--out-dir <dir>` defaults to `.c2/test-results/<timestamp>/`
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

### Execute a dispatched lease

When a dispatcher has already claimed work, use its protected capability file:

```bash
c2j run with-lease --jobdb https://jobdb.example.com/tenant --job-id JOB_ID --lease-file /run/secrets/job-lease.json
```

Use `--lease-file -` for redirected stdin. The command validates and renews the
supplied lease, runs the usual execution lifecycle, and exits on completion,
rescheduling, or lease loss without claiming another lease. The file must be
owned by the worker user with no group/other permissions. See the
[supplied-lease guide](GUIDE-Run-With-Lease.md) for dispatcher/library integration,
exit behavior, and the JobDB v0.0.22 server requirement.

### Deleting jobs

```bash
c2j delete --self --older-than 720h --dry-run
c2j delete --cell backend --cell frontend --recipe build
c2j delete --job-id JOB1 --job-id JOB2 --json
c2j delete --created-before 2026-10-01T00:00:00Z --dry-run
c2j delete --all --embed --dry-run
```

Use the usual `--jobdb`, environment, or project configuration to select a tenant;
`--embed` uses local JobDB. Deletion requires an explicit selector or `--all`.
It does not implicitly select the current cell: use `--self` for that.
`--cell` matches the owning repository across all refs, as `c2j list` does;
workspace overrides do not change job ownership. Repeat `--cell` or `--job-id`
to select any of those values. Different selectors intersect.

`--created-before` is an exclusive creation-time cutoff, not a completion-time
cutoff. Alternatively, `--older-than` takes a positive Go duration (`720h` is
30 days); the cutoff is calculated once per invocation. `--all` cannot combine
with selectors. Status filtering is not supported; matching unfinished jobs
are included.

`--dry-run` reports matches without deleting; it does not reserve those matches
for a later call. Both text and JSON output include resolved selection criteria
and matched/deleted counts. Missing and already-hidden jobs do not count.

Deletion hides jobs from normal job and schedule-run listings, cancels unfinished
jobs, and revokes their leases. Completed outcomes, history, and artifacts remain
accessible by direct lookup and to surviving clones. It does not reclaim disk
space, cascade to children/clones/schedules, undo external effects, or forcibly
stop worker processes. This uses JobDB's bulk deletion API; remote servers must
support it (JobDB `20269bd` / `v0.0.46` or later, with a compatible backend).
Unsupported backends return an error; c2j never falls back to listing and deleting
jobs individually.

### Recipe timeouts and recovery

c2j does not set default job or built-in op timeouts. Set `timeout` on a recipe
or node to bound its execution. Command `inputs.timeout` and extension manifest
`timeout` remain supported; an enclosing deadline can end execution sooner.

For pending external input, a late response can be accepted until a worker claims
the timeout work. Once the timeout wins, later responses are rejected. Accepting
a late response does not extend an enclosing recipe or scope deadline.

Recipe and nested execution timeouts retain their original scope budget across
worker recovery. Cached completed tasks remain replayable; new work does not
receive a fresh budget merely because execution resumed. See
[timeout recovery](TIMEOUT_RECOVERY.md) for timing semantics, deployment
compatibility, and the remaining upstream JobDB timeout limitations.

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

## Dispatching work

Run a known job with `c2j run --job-id <id>`, or let a dispatcher such as Pulse
claim a lease and launch [`c2j run with-lease`](GUIDE-Run-With-Lease.md).
`run any` and `run loop` are no longer supported.

`c2j ready` prints a read-only snapshot of the number of ready recipe jobs:

```bash
c2j ready --jobdb http://localhost:9047/<tenant-id>
```

The count is not a reservation and may change before a dispatcher claims work.

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
- runtime root: `~/.c2/embed/default`

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

- `--advanced-recipe` and `--advanced-recipe-file` are mutually exclusive on `submit`
- `--json` and `--run` are mutually exclusive on `submit`
- `--inputs-json` and `--inputs-file` are mutually exclusive
- `--self` and `--cell` are mutually exclusive
- `--artifact` names must be unique relative paths; directories are not supported yet
- `self`, `cells`, and implicit current-cell submission depend on config or supported auto-detection succeeding
- short cell names require a config pattern; without config, use an explicit repo or path
- `submit --advanced-recipe-file` is clearer than passing a local file path through `--advanced-recipe`

## Related Files

- Git authentication and URL routing: [Git profiles](GIT_PROFILES.md)
- separate Git operations: [squash, rebase, and fast-forward-only merge](GUIDE-Git-Ops.md)

- command entrypoint: [main.go](main.go)
- embedded runtime notes: [embed-swf-mode-spec.md](embed-swf-mode-spec.md)
- recipe authoring docs: [RECIPE_AUTHORING_GUIDE.md](../../recipes/guides/RECIPE_AUTHORING_GUIDE.md)

## Op object checkpoints

Ops can publish immutable typed objects containing metadata and files, then accept an explicit reference to any earlier checkpoint. Each consumer receives a fresh writable copy. See [the API and Codex migration guide](GUIDE-Op-Object-Checkpoints.md) for recipe routing, extension manifests, native APIs, and compatibility details.
