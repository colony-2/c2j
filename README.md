# c2j

`c2j` is the local job-oriented CLI for submitting and running recipe jobs through an SWF runtime.

Use it when you want to:

- submit a recipe job from a named recipe or a local recipe file
- run or continue an existing job
- use the embedded local runtime for fast iteration
- inspect the current cell configuration used for job targeting
- list jobs for a cell

Examples below assume you are running from the repo root.

## Command Summary

```bash
c2j self
c2j cells
c2j init
c2j submit
c2j exec
c2j list
c2j test
```

Use `go run ./cmd/c2j --help` or `c2j --help` to see the full command tree.

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

### 2. Submit and run a local recipe file

For local authoring, this is the default loop:

```bash
c2j submit \
  --recipe-file ./recipes/my-recipe.yaml \
  --run \
  --embed
```

That does all of the following:

- loads the local YAML file
- embeds that recipe into the submitted job
- starts an embedded SWF runtime
- submits the job
- immediately executes it

### 3. Continue or inspect a job later

Find recent jobs for the current cell:

```bash
c2j list --self --embed
```

Continue a submitted job:

```bash
c2j exec --job-id <job-id> --embed
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

Writes a commented `.c2j/config.yaml` template.

```bash
c2j init
c2j init --stdout
c2j init --force
```

The generated template can derive values from the Go module in the current repo when `base: go` is appropriate.

## Submitting Jobs

### Basic forms

Submit a named recipe:

```bash
c2j submit --recipe default --embed
```

`--recipe` accepts a recipe name or git selector. For local files, prefer `--recipe-file`.

Submit a local recipe file:

```bash
c2j submit --recipe-file ./recipes/my-recipe.yaml --embed
```

Submit and run immediately:

```bash
c2j submit --recipe-file ./recipes/my-recipe.yaml --run --embed
```

By default, if neither `--recipe` nor `--recipe-file` is set, `c2j` submits the recipe named `default`.

### Passing inputs

Inline JSON:

```bash
c2j submit \
  --recipe-file ./recipes/my-recipe.yaml \
  --inputs-json '{"message":"hello"}' \
  --run \
  --embed
```

Inputs file in JSON or YAML:

```bash
c2j submit \
  --recipe-file ./recipes/my-recipe.yaml \
  --inputs-file ./recipes/test-inputs.yaml \
  --run \
  --embed
```

Positional prompt shortcut:

```bash
c2j submit "Summarize the repo" --recipe my-prompt-recipe --embed
```

The positional argument is merged as `inputs.prompt`.

Rules:

- `--inputs-json` and `--inputs-file` are mutually exclusive
- the positional prompt cannot also be provided as `inputs.prompt`

### Choosing the target cell

Use the current cell:

```bash
c2j submit --recipe-file ./recipes/my-recipe.yaml --self --embed
```

Use another cell explicitly:

```bash
c2j submit \
  --recipe-file ./recipes/my-recipe.yaml \
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
c2j submit \
  --recipe-file ./recipes/my-recipe.yaml \
  --json \
  --embed
```

This emits:

```json
{
  "tenant_id": "0",
  "job_id": "job-...",
  "recipe": "my_recipe_id"
}
```

Note:

- `--json` and `--run` are mutually exclusive

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
- passthrough cases use a disposable embedded runtime root automatically; use `--runtime-root` and `--keep-runtime` only for debugging

## Running Jobs

`c2j exec` executes or continues an existing job and prints live story progress to stdout.

Basic usage:

```bash
c2j exec --job-id <job-id> --embed
```

Common variants:

```bash
c2j exec --job-id <job-id> --wait-timeout 30m --embed
c2j exec --job-id <job-id> --input-mode fail --embed
c2j exec --job-id <job-id> --ci --embed
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

`--on-not-ready` controls how `exec` reacts when a job is not runnable yet:

- `wait`
- `fail`
- `fail-on-lease`
- `fail-on-pending-jobs`
- `fail-on-future`
- `fail-on-missing-capability`

With the default `wait`, `exec` will print `waiting: ...` lines and poll until the job becomes runnable or the wait timeout is reached.

### Exit codes

`c2j exec` uses distinct exit codes:

- `1`: general failure or job failure
- `2`: wait timeout
- `3`: input required
- `4`: job not runnable under the selected policy
- `5`: invalid job identity or invalid exec arguments

## Listing Jobs

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

## Embedded Runtime

`--embed` is shorthand for:

```bash
--swf-url embed:///
```

Use it when you want a local self-contained runtime instead of an external SWF server.

Behavior:

- starts embedded Postgres and Strata as needed
- uses a persistent runtime root on disk
- works well for local recipe authoring and debugging

Defaults:

- runtime URL: `embed:///`
- runtime root: `~/.c2j/embed/default`

You can override the root with:

```bash
export C2J_EMBED_ROOT=/absolute/path/to/embed-root
```

Notes:

- `C2J_EMBED_ROOT` must be an absolute path
- only one `c2j` process can own a given embedded runtime root at a time
- if you need parallel embedded runtimes, give each process a different `C2J_EMBED_ROOT`

## Runtime and Tenant Defaults

`c2j` reads these environment variables:

- `C2J_SWF_URL`
- `C2J_TENANT_ID`
- `C2J_EMBED_ROOT`

Built-in defaults:

- `C2J_SWF_URL`: `http://localhost:9047`
- `C2J_TENANT_ID`: `0`

Examples:

```bash
export C2J_SWF_URL=http://localhost:9047
export C2J_TENANT_ID=123
```

When `--embed` is present, it overrides `C2J_SWF_URL` for that command.

## Common Workflows

### Local recipe authoring loop

```bash
c2j self
c2j submit --recipe-file ./recipes/my-recipe.yaml --run --embed
```

### Detached submit, then later run

```bash
c2j submit --recipe-file ./recipes/my-recipe.yaml --json --embed
c2j exec --job-id <job-id> --embed
```

### Run against a remote runtime instead of embed

```bash
c2j submit \
  --recipe-file ./recipes/my-recipe.yaml \
  --swf-url http://localhost:9047 \
  --run
```

### Target another cell explicitly

```bash
c2j submit \
  --recipe-file ./recipes/my-recipe.yaml \
  --cell github.com/colony-2/root \
  --run \
  --embed
```

## Gotchas

- `--recipe` and `--recipe-file` are mutually exclusive
- `--json` and `--run` are mutually exclusive on `submit`
- `--inputs-json` and `--inputs-file` are mutually exclusive
- `--self` and `--cell` are mutually exclusive
- `self`, `cells`, and implicit current-cell submission depend on config or supported auto-detection succeeding
- short cell names require a config pattern; without config, use an explicit repo or path
- `--recipe-file` is clearer than passing a local file path through `--recipe`

## Related Files

- command entrypoint: [main.go](main.go)
- embedded runtime notes: [embed-swf-mode-spec.md](embed-swf-mode-spec.md)
- recipe authoring docs: [RECIPE_AUTHORING_GUIDE.md](../../recipes/guides/RECIPE_AUTHORING_GUIDE.md)
