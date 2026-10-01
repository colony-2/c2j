<!-- Sources: README.md; cmd/c2j/internal/cmd/*.go; cmd/c2j/internal/submitjob/options.go; cmd/c2j/internal/runjob/options.go; cmd/c2j/internal/testjob/options.go. -->

# Testing And CLI

## Local Authoring Loop

Use embedded JobDB for local smoke tests:

```bash
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/my-recipe.yaml --run --embed
```

This loads the local YAML file, embeds it into the submitted job, starts an embedded runtime, submits the job, and runs it immediately.

For ordinary jobs, use `c2j submit "your prompt"` (build) or add `--evolve`. The existing resolver looks up `build`/`evolve` in the target cell, with missing-file fallback to the same-named root YAML on `main` in `colony-2/recipes`. Custom files/selectors use `--advanced-recipe-file`/`--advanced-recipe`; do not combine these with `--build`/`--evolve`.

Conventional build/evolve recipes receive `{"prompt":"your prompt","type":"build"}` or the corresponding `"evolve"` type. Declare both as required strings in `input_schema`, and bind them in root `inputs` when using them in recipe logic. This also applies to explicit selection of the names `build` and `evolve`; custom names, Git selectors, and files have no automatic type injection. User-provided types must match the conventional selection.

Every CLI submission supplies `inputs.prompt`, so custom recipes must declare a string `prompt` in `input_schema`. When omitted from arguments and input data, a terminal prompts interactively; non-terminal stdin produces an error. `c2j test` retains its flags and does not require a prompt unless the recipe's schema does.

## Inputs

Inline JSON:

```bash
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/my-recipe.yaml --inputs-json '{"message":"hello"}' --run --embed
```

File input in JSON or YAML:

```bash
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/my-recipe.yaml --inputs-file ./recipes/inputs.yaml --run --embed
```

Positional prompt shortcut:

```bash
c2j submit "Summarize the repo" --advanced-recipe my-prompt-recipe --embed
```

`--inputs-json` and `--inputs-file` are mutually exclusive. The positional prompt merges as `inputs.prompt` and cannot be combined with an explicit `inputs.prompt`.

## Artifacts

Attach files with repeatable artifact flags:

```bash
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/review.yaml --artifact ./brief.md --artifact requirements=./requirements.md --run --embed
```

`--artifact PATH` uses the basename. `--artifact NAME=PATH` sets an explicit name.

## `c2j test`

Use `c2j test` for scenario suites and fixture-style validation.

Prefer self-contained suites with a top-level `recipe` path relative to the
suite file. Run a repository with `c2j test run --directory recipe-tests`;
validate it with the same `--directory` flag. No manifest or shell runner is
needed. Discovery recognizes `*.test.yaml`, `*.test.yml`, `*.test.json`, and
`*.scenario.md`; every discovered suite must contain executable cases. Declare
`live: true` for live integrations and select them explicitly with
`--include-live`. See `GUIDE-Native-Recipe-Testing.md` for reports and selection.

Use `runtime: {}` on a case when testing real child jobs, durable artifacts,
workspace changes, objects or input/review responses. c2j owns the disposable
runtime and workers. Declare model responses with existing op mocks; runtime
fixture effects can publish objects/artifacts, edit worktree files and submit
fixture children through the broker. Answer real input ops with
`runtime.responses`, including ordinary attachment fields. Use `cel_true` over
`outputs`, `calls`, `reviews`, `artifacts`, and `status` to assert recipe behavior
without modifying production recipe graphs. Runtime guarantees such as restart
replay and lease handling belong in c2j's own tests, not recipe-specific servers
or Python/shell orchestration.

Common flags from this checkout:

- `--recipe` or `--recipe-file`
- `--file` or `--stdin` for the scenario suite
- `--format`
- `--case`
- `--strict`
- `--parallelism`
- `--fail-fast`
- `--stop-on-failure`
- `--execution-timeout`
- `--artifact-mode`
- `--artifact-max-bytes`
- `--evaluation-mode`
- `--out`
- `--out-dir`
- `--jsonl-events`
- `--json`

## Job Inspection

List recent jobs:

```bash
c2j list --self --embed
c2j list --self --embed --json
```

Continue a job:

```bash
c2j run --job-id <job-id> --embed
```

List child jobs when the command is available:

```bash
c2j list children --jobdb https://jobdb.example.com/dev --parent-tenant-id dev --parent-job-id <job-id> --all-ops
```

## Targeting

Use the current cell:

```bash
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/my-recipe.yaml --self --embed
```

Use another cell:

```bash
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/my-recipe.yaml --cell github.com/colony-2/root --embed
```

`--self` and `--cell` are mutually exclusive.

## Validation Before Hand-Off

For recipe edits, run the narrowest applicable validation:

```bash
go test ./pkg/recipe ./pkg/template ./pkg/worker/...
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/my-recipe.yaml --run --embed
```

If the user supplied a test scenario, prefer `c2j test` with that scenario over a generic run.
