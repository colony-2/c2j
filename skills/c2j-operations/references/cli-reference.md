<!-- Sources: README.md; cmd/c2j/internal/cmd/*.go; cmd/c2j/internal/*/options.go. -->

# CLI Reference

## Commands

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
c2j list children
c2j test
```

Use `c2j --help` or `go run ./cmd/c2j --help` for the complete command tree.

## `c2j init`

Writes a commented `.c2j/config.yaml` template and installs bundled c2j skills unless disabled.

```bash
c2j init
c2j init --stdout
c2j init --force
c2j init --no-skills
c2j init --skills-force
c2j init --skills-scope auto
c2j init --skills-scope project
c2j init --skills-scope user
c2j init --with-op-skills
```

`--stdout` prints only the config and does not install skills.

## `c2j submit`

Submit a build (default) or evolve job. A non-empty prompt is required: pass it positionally or as `inputs.prompt`, or enter it interactively in a terminal. Missing prompts in non-terminal mode fail immediately.

```bash
c2j submit "Implement the endpoint" --embed
c2j submit "Improve the retry behavior" --evolve --embed
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/my-recipe.yaml --embed
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/my-recipe.yaml --run --embed
```

Key flags:

- `--jobdb`
- `--build` (default)
- `--evolve`
- `--advanced-recipe`
- `--advanced-recipe-file`
- `--inputs-json`
- `--inputs-file`
- `--artifact`
- `--self`
- `--cell`
- `--run`
- `--embed`
- `--json`

The recipe-selection flags are mutually exclusive. `--json` and `--run` are mutually exclusive. `--recipe` and `--recipe-file` are hidden, deprecated aliases on `submit`.

Normal selection resolves the name `build` or `evolve` at execution time in the target cell's repository/ref (`.c2j/recipes/build.yaml` or `evolve.yaml`). Only a missing recipe falls back to `git+https://github.com/colony-2/recipes.git//build.yaml@main` or `evolve.yaml@main`. `--cell` never uses the submitting project's recipes. Named recipes use committed content; explicit advanced files embed local edits. All CLI-submitted recipes must declare `prompt` in `input_schema`.

Build/evolve submissions automatically include `inputs.type` (`"build"` or `"evolve"`) alongside `inputs.prompt`, including explicit selection of those names. Both conventional recipes must declare a string `type` in `input_schema`. A supplied type must match the selection; it does not select a mode. Custom names, explicit Git selectors, and local files preserve their own input contracts without automatic type injection.

## `c2j run`

Continue a job.

```bash
c2j run --job-id <job-id> --embed
```

Useful flags include `--on-not-ready`, `--input-mode`, `--wait-timeout`, `--poll-interval`, and `--lease-duration`.

Supported `--input-mode` values are `prompt`, `ops`, and `fail`.

Supported `--on-not-ready` values include `wait`, `fail`, `fail-on-lease`, `fail-on-pending-jobs`, `fail-on-future`, and `fail-on-missing-capability`.

## `c2j list`

List jobs for a cell.

```bash
c2j list --self --embed
c2j list --self --embed --json
```

Filter with `--status`, `--job-type`, `--job-id`, `--waiting-for`, `--created-after`, `--created-before`, `--page-size`, `--page-token`, and `--all` where exposed by the command.

## `c2j test`

Compile, validate, and run scenario suites.

```bash
c2j test --file ./recipes/tests/my.scenario.md --recipe-file ./recipes/my-recipe.yaml
```

Use `--json` or `--jsonl-events` when consuming results programmatically.
