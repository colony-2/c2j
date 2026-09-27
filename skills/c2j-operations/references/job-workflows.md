<!-- Sources: README.md; cmd/c2j/internal/submitjob/options.go; cmd/c2j/internal/runjob/options.go; cmd/c2j/internal/listjobs/options.go; cmd/c2j/internal/workjob/options.go. -->

# Job Workflows

## Local Submit And Run

```bash
c2j submit "Implement the endpoint" --run --embed
c2j submit "Improve retry behavior" --evolve --run --embed
```

These resolve `build` (default) or `evolve` in the target cell, falling back to the shared recipes when absent. Embedded runtime avoids a remote JobDB. For custom local recipe authoring, use `--advanced-recipe-file ./recipes/my-recipe.yaml` instead of a mode flag and declare `prompt` in the recipe's input schema.

## Submit Then Continue Later

```bash
c2j submit "Implement the endpoint" --embed --json
c2j run --job-id <job-id> --embed
```

Use JSON output to capture the submitted job identity. Use `c2j list --self --embed` if the job ID is not known.

## Ready And Worker Commands

Use `ready` to count claimable work for schedulers:

```bash
c2j ready --embed
```

Use worker commands to run claim loops:

```bash
c2j run one --embed
c2j run any --embed
c2j run loop --embed
```

Prefer bounded local concurrency and explicit tenant/cell targeting in automation.

## Input Modes

`c2j run` supports:

- `prompt`: ask interactively for user input.
- `ops`: allow input collection through operation mechanisms.
- `fail`: fail when input is required.

In CI or non-terminal stdin, the default input mode is `ops`.

## Exit Behavior

Command services return non-zero exit codes for usage errors, compile/test failures, and run failures through typed exit errors. When scripting, prefer `--json` outputs where available and check process status.

## Child Jobs

Use child listing commands to inspect jobs started by a parent:

```bash
c2j list children --job-id <job-id> --embed
```

In recipes, child jobs started through recipe ops and child groups are also exposed in runtime context where supported by the worker.
