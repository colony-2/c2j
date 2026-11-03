<!-- Sources: README.md; cmd/c2j/internal/cmd/*.go; cmd/c2j/internal/submitjob/options.go; cmd/c2j/internal/submitjob/workspace.go; pkg/workspacesubmit/*.go; pkg/childbroker/workspace.go; cmd/c2j/internal/runjob/options.go; cmd/c2j/internal/testjob/options.go. -->

# Testing And CLI

## Discover Current Execution

Run `c2j self` at the start of a task to discover job/task identity, workspace,
lease status, effective permissions, and available c2j actions. Use
`c2j self --json` for complete identifiers and structured details. Follow the
reported routes and restrictions; recheck after a lease or permission failure.
Unknown means unverified, not allowed or denied. Permission and route availability
are separate, and lease validity is last known rather than freshly validated.

Outside a task the report says standalone; configured JobDB credentials are not
inspected. A missing or incompatible helper inside a task reports unknown
authority without falling back to standalone credentials. Normal absence of both
cell and task context returns a partial report with exit status 0.

## Local Authoring Loop

Use embedded JobDB for local smoke tests:

```bash
c2j submit --recipe-file ./recipes/my-recipe.yaml --run --embed
```

This loads the local YAML file, embeds it into the submitted job, starts an embedded runtime, submits the job, and runs it immediately.

## Starting From Workspace State

Choose the starting state explicitly; `--self` selects a cell and does not capture
workspace changes. Without either workspace flag, `submit` uses the target cell's
configured ref, usually `main`.

- `--from-workspace` reuses the immutable incoming snapshot of the current task
  invocation's active workspace. It includes prior tasks' persisted changes and
  excludes commits or edits made during this task. It does not inspect the live
  worktree. This requires a running c2j task with a workspace-aware broker.
- `--from-workspace-now` uses current HEAD and file contents. It includes unpublished
  commits, staged and unstaged tracked changes, deletions, and non-ignored untracked
  files. It reuses an unchanged incoming snapshot and captures changed contents
  when needed. A clean worktree with a newer HEAD uses that commit without creating
  an extra commit. Staging boundaries are not preserved in the submitted snapshot.

```bash
# Inside a running task: start from the state supplied to that task.
c2j submit "Review this task's starting state" --from-workspace

# Inside a task or from a local Git worktree: include current changes.
c2j submit "Continue with these changes" --from-workspace-now
```

The flags are mutually exclusive and work with default and custom recipes. Outside
a running task, use `--from-workspace-now`; `--from-workspace` fails because there
is no task-start snapshot. Inside a task, both modes use its active workspace,
including a workspace override, rather than inferring it from the CLI's directory.
An omitted target or `--self` selects that workspace's cell; an explicit `--cell`
must match its repository and cell identity.

Neither mode pushes changes or changes the submitting worktree, index, HEAD, or
branch refs. Submitted state stays fixed across later local edits and retries.
Live capture rejects unresolved merge conflicts and requires a worktree that is
not being concurrently modified; task-start reuse can proceed despite live edits
or conflicts. Missing context or snapshots and target mismatches fail without
falling back to the configured ref. Upgrade the CLI and workers together; the CLI
checks broker support before submitting with either flag inside a task.

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
