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
c2j run with-lease
c2j ready
c2j list
c2j list children
c2j delete
c2j test
```

Use `c2j --help` or `go run ./cmd/c2j --help` for the complete command tree.

## `c2j init`

Writes a commented `.c2/config.yaml` template and installs bundled c2j skills unless disabled.

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

Submit a build job. A non-empty prompt is required: pass it positionally or as `inputs.prompt`, or enter it interactively in a terminal. Missing prompts in non-terminal mode fail immediately.

```bash
c2j submit "Implement the endpoint" --embed
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/my-recipe.yaml --embed
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/my-recipe.yaml --run --embed
```

Key flags:

- `--jobdb`
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

Normal selection resolves the name `build` at execution time in the target cell's repository/ref (`.c2/recipes/build.yaml`). Only a missing recipe falls back to `git+https://github.com/colony-2/recipes.git//build.yaml@main`. `--cell` never uses the submitting project's recipes. Named recipes use committed content; explicit advanced files embed local edits. All CLI-submitted recipes must declare `prompt` in `input_schema`.

Build submissions automatically include `inputs.type` (`"build"`) alongside `inputs.prompt`, including explicit selection of that name. The build recipe must declare a string `type` in `input_schema`. A supplied type must be `"build"`; it does not select a recipe. Custom names, explicit Git selectors, and local files preserve their own input contracts without automatic type injection.

## `c2j run`

Continue a job.

```bash
c2j run --job-id <job-id> --embed
```

Useful flags include `--on-not-ready`, `--input-mode`, `--wait-timeout`, `--poll-interval`, and `--lease-duration`.

Supported `--input-mode` values are `prompt`, `ops`, and `fail`.

Supported `--on-not-ready` values include `wait`, `fail`, `fail-on-lease`, `fail-on-pending-jobs`, `fail-on-future`, and `fail-on-missing-capability`.

## `c2j run with-lease`

Execute a lease already claimed by a dispatcher:

```bash
c2j run with-lease --jobdb https://jobdb.example.com/tenant --job-id JOB_ID --lease-file /run/secrets/job-lease.json
```

Use `--lease-file -` for protected redirected stdin. Named files must be regular,
not symlinks, owned by the current user, and have no group/other permissions.
Both the job ID and configured tenant must match the capability. JobDB v0.0.22
server renewal support is required.

This command never claims work. It has allocation flags and `--await-threshold`,
but no `--worker-id`, `--lease-duration`, wait/poll controls, or `--embed`.
`--input-mode` supports `ops` (default) or `fail`; both exit 3 when input is
pending. Successful rescheduling exits 0 with a handoff/suspension event, which
does not mean the whole job completed. See [job workflows](job-workflows.md).

## `c2j list`

List jobs for a cell.

```bash
c2j list --self --embed
c2j list --self --embed --json
```

Filter with `--status`, `--job-type`, `--job-id`, `--waiting-for`, `--created-after`, `--created-before`, `--page-size`, `--page-token`, and `--all` where exposed by the command.

## `c2j delete`

Delete jobs through one server-side bulk request:

```bash
c2j delete --self --older-than 720h --dry-run
c2j delete --cell backend --cell frontend --recipe build
c2j delete --job-id JOB1 --job-id JOB2 --json
```

Requires explicit selectors or `--all` (the entire selected tenant, incompatible
with selectors). No implicit current-cell selection. Cells/IDs are repeatable;
different selectors intersect. `--created-before` is an exclusive RFC3339
creation-time cutoff; `--older-than` uses a positive Go duration. Both age flags
are mutually exclusive. `--jobdb` and `--embed` follow the usual conventions.
Status filtering is unavailable; unfinished matches are included.

`--dry-run` previews counts, without reserving matches. Deletion hides jobs,
cancels unfinished work, and revokes leases; completed outcomes, history, and
artifacts remain accessible. No storage reclamation or child/clone/schedule
cascade. A compatible JobDB deletion API/backend is required.

## `c2j test`

Compile, validate, and run scenario suites.

```bash
c2j test --file ./recipes/tests/my.scenario.md --recipe-file ./recipes/my-recipe.yaml
```

Use `--json` or `--jsonl-events` when consuming results programmatically.
