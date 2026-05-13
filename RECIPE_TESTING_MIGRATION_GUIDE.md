# Recipe Testing Migration Guide

This guide is for users moving from the old Colony2 recipe testing command:

```bash
c2 recipe test ...
```

to the new local `c2j` testing command:

```bash
c2j test ...
```

## What Changed

Recipe tests now run locally inside `c2j`.

The old flow was:

```text
c2 CLI -> Colony2 API -> recipe test server handler -> recipe test execution
```

The new flow is:

```text
c2j test -> local suite compiler -> local test harness
```

There is no API server call, no persisted server-side test run, and no `POST /recipe-tests/...` request.

## Command Mapping

| Old command | New command |
| --- | --- |
| `c2 recipe test compile` | `c2j test compile` |
| `c2 recipe test validate` | `c2j test validate` |
| `c2 recipe test run` | `c2j test run` |
| `c2 recipe test case validate` | `c2j test case validate` |
| `c2 recipe test case run` | `c2j test case run` |

Most suite, case, and output flags keep the same meaning.

## Target Recipe Migration

### Local Recipe Files

This is the recommended migration path for local authoring.

Old:

```bash
c2 recipe test run \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./recipes/my-recipe.test.yaml
```

New:

```bash
c2j test run \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./recipes/my-recipe.test.yaml
```

### Named Recipes

Old named recipes were looked up through Colony2 server recipe storage using `--recipe` plus `--version` or `--ref`.

New named recipes resolve from the current c2j cell:

```bash
c2j test run \
  --recipe default \
  --file ./recipes/default.test.yaml
```

That expects current-cell resolution to work from `.c2j/config.yaml` or supported auto-detection. The recipe name maps to:

```text
.c2j/recipes/<name>.yaml
```

For example, `--recipe default` resolves to `.c2j/recipes/default.yaml` in the target cell.

### Git Selectors

You can also target an explicit git selector:

```bash
c2j test run \
  --recipe 'git+https://github.com/acme/demo.git//.c2j/recipes/default.yaml@main' \
  --file ./recipes/default.test.yaml
```

### Removed Target Flags

These old server lookup flags do not apply to `c2j test`:

- `--version`
- `--ref`

Use a git selector when you need an exact ref, or set the cell ref in `.c2j/config.yaml`.

## Suite Inputs

The suite formats are intentionally carried over:

- `canonical_yaml`
- `canonical_json`
- `compact_yaml`
- `scenario_md`

Examples:

```bash
c2j test run --recipe-file ./recipes/my-recipe.yaml --file ./tests/suite.yaml
c2j test run --recipe-file ./recipes/my-recipe.yaml --file ./tests/suite.scenario.md
c2j test run --recipe-file ./recipes/my-recipe.yaml --stdin < ./tests/suite.yaml
```

You can still select cases:

```bash
c2j test run \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./tests/suite.yaml \
  --case smoke \
  --case regression
```

Single-case commands use `--case-id`:

```bash
c2j test case run \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./tests/suite.yaml \
  --case-id smoke
```

## Compile

Old:

```bash
c2 recipe test compile \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./tests/suite.yaml \
  --out ./tmp/compiled.json
```

New:

```bash
c2j test compile \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./tests/suite.yaml \
  --out ./tmp/compiled.json
```

The compiled target mode is now local:

- `inline_recipe` for `--recipe-file`
- `recipe_selector` for named recipes and git selectors

The old `server_ref` target mode is not used by `c2j test`.

## Validate

Old validation called the API once per case.

New validation is local:

```bash
c2j test validate \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./tests/suite.yaml
```

Useful flags:

- `--parallelism <n>`
- `--fail-fast`
- `--case <id>`
- `--json`

## Run

Run a suite locally:

```bash
c2j test run \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./tests/suite.yaml
```

Useful flags:

- `--parallelism <n>`
- `--stop-on-failure`
- `--case-timeout <duration>`
- `--artifact-mode none|inline`
- `--artifact-max-bytes <n>`
- `--evaluation-mode enforce|report_only`
- `--jsonl-events <path>`
- `--out-dir <dir>`

Default output directory:

```text
.c2j/test-results/<timestamp>/
```

Output layout:

```text
summary.json
summary.md
cases/<case-id>/result.json
cases/<case-id>/artifacts/*
cases/<case-id>/evaluations.json
```

## Runtime Behavior

Normal mocked recipe tests do not submit SWF jobs.

For tests that require runtime-backed passthrough behavior, `c2j test` uses embedded runtime mode automatically. You do not need:

```bash
--embed
```

By default, test runtime state is disposable and per run. It does not use the normal persistent embedded root:

```text
~/.c2j/embed/default
```

Debug flags:

- `--runtime-root <path>` uses a specific embedded runtime root
- `--keep-runtime` keeps the temporary runtime root after the run

`C2J_SWF_URL` is not part of the normal test loop.

## Case Behavior Preserved

The local harness preserves the old core test behavior:

- op mocks
- duplicate mock consumption rules
- isolated policy requiring mocks by default
- op-case node scope enforcement
- assertions
- text-pattern evaluations
- report-only vs enforced evaluation mode
- inline artifact capture and truncation
- diagnostics for mock hits and misses

## Exit Behavior

Typical exit behavior:

- `0`: selected cases validated or passed
- non-zero: compile error, invalid options, local runtime/setup error, invalid case, failed case, errored case, or timed-out case

The old network/API error category is gone because the new command does not call the Colony2 API.

## Common Migration Examples

### Local Smoke Test

Old:

```bash
c2 recipe test run \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./recipes/my-recipe.test.yaml \
  --case smoke
```

New:

```bash
c2j test run \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./recipes/my-recipe.test.yaml \
  --case smoke
```

### Run With Inline Artifacts

Old:

```bash
c2 recipe test run \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./recipes/my-recipe.test.yaml \
  --artifact-mode inline \
  --artifact-max-bytes 65536
```

New:

```bash
c2j test run \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./recipes/my-recipe.test.yaml \
  --artifact-mode inline \
  --artifact-max-bytes 65536
```

### Current-Cell Default Recipe

Old server ref style:

```bash
c2 recipe test run \
  --recipe default \
  --ref main \
  --file ./recipes/default.test.yaml
```

New current-cell style:

```bash
c2j test run \
  --recipe default \
  --file ./recipes/default.test.yaml
```

If you need an explicit ref:

```bash
c2j test run \
  --recipe 'git+https://github.com/acme/demo.git//.c2j/recipes/default.yaml@main' \
  --file ./recipes/default.test.yaml
```

## Troubleshooting

If a named recipe cannot be found:

- run `c2j self` to verify current-cell resolution
- verify the recipe exists at `.c2j/recipes/<name>.yaml`
- use `--recipe-file` while authoring locally
- use an explicit git selector when testing a specific ref

If passthrough tests are flaky:

- use `--runtime-root <path> --keep-runtime` to inspect embedded runtime state
- delete the debug runtime root before retrying if you want a clean run

If output files are missing:

- check whether the run exited before scheduling the case
- set `--out-dir <dir>` explicitly
- use `--artifact-mode inline` when you expect artifact files under `cases/<case-id>/artifacts/`
