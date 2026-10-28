<!-- Sources: README.md; cmd/c2j/internal/cmd/*.go; cmd/c2j/internal/submitjob/options.go; cmd/c2j/internal/runjob/options.go; cmd/c2j/internal/testjob/options.go. -->

# Testing And CLI

## Local Authoring Loop

Use embedded JobDB for local smoke tests:

```bash
c2j submit --recipe-file ./recipes/my-recipe.yaml --run --embed
```

This loads the local YAML file, embeds it into the submitted job, starts an embedded runtime, submits the job, and runs it immediately.

## Inputs

Inline JSON:

```bash
c2j submit --recipe-file ./recipes/my-recipe.yaml --inputs-json '{"message":"hello"}' --run --embed
```

File input in JSON or YAML:

```bash
c2j submit --recipe-file ./recipes/my-recipe.yaml --inputs-file ./recipes/inputs.yaml --run --embed
```

Positional prompt shortcut:

```bash
c2j submit "Summarize the repo" --recipe my-prompt-recipe --embed
```

`--inputs-json` and `--inputs-file` are mutually exclusive. The positional prompt merges as `inputs.prompt` and cannot be combined with an explicit `inputs.prompt`.

## Artifacts

Attach files with repeatable artifact flags:

```bash
c2j submit --recipe-file ./recipes/review.yaml --artifact ./brief.md --artifact requirements=./requirements.md --run --embed
```

`--artifact PATH` uses the basename. `--artifact NAME=PATH` sets an explicit name.

## `c2j test`

Use `c2j test` for scenario suites and fixture-style validation.

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
c2j list children --job-id <job-id> --embed
```

## Targeting

Use the current cell:

```bash
c2j submit --recipe-file ./recipes/my-recipe.yaml --self --embed
```

Use another cell:

```bash
c2j submit --recipe-file ./recipes/my-recipe.yaml --cell github.com/colony-2/root --embed
```

`--self` and `--cell` are mutually exclusive.

## Validation Before Hand-Off

For recipe edits, run the narrowest applicable validation:

```bash
go test ./pkg/recipe ./pkg/template ./pkg/worker/...
c2j submit --recipe-file ./recipes/my-recipe.yaml --run --embed
```

If the user supplied a test scenario, prefer `c2j test` with that scenario over a generic run.
