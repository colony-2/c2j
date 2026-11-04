<!-- Sources: README.md; cmd/c2j/internal/cmd/*.go; cmd/c2j/internal/submitjob/options.go; cmd/c2j/internal/submitjob/workspace.go; pkg/workspacesubmit/*.go; pkg/childbroker/workspace.go; cmd/c2j/internal/runjob/options.go; cmd/c2j/internal/testjob/options.go. -->

# Testing And CLI

## Local Authoring Loop

Use embedded JobDB for local smoke tests:

```bash
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/my-recipe.yaml --run --embed
```

This loads the local YAML file, embeds it into the submitted job, starts an embedded runtime, submits the job, and runs it immediately.

For ordinary jobs, use `c2j submit "your prompt"` to submit the build recipe. The default selector is `git+https://github.com/colony-2/recipes.git//build.yaml@main`, which skips target-cell recipe lookup. Explicit bare names still resolve in the target cell; use `--advanced-recipe build` for its `.c2/recipes/build.yaml` (with the existing missing-file shared fallback). Custom files/selectors use `--advanced-recipe-file`/`--advanced-recipe`; these flags are mutually exclusive.

Build recipes receive `{"prompt":"your prompt","type":"build"}`. Declare both as required strings in `input_schema`, and bind them in root `inputs` when using them in recipe logic. This also applies to explicit selection of the name `build`; custom names, Git selectors, and files have no automatic type injection. For build submissions, a user-provided type must be `"build"`.

Every CLI submission supplies `inputs.prompt`, so custom recipes must declare a string `prompt` in `input_schema`. When omitted from arguments and input data, a terminal prompts interactively; non-terminal stdin produces an error. `c2j test` retains its flags and does not require a prompt unless the recipe's schema does.

Use `execution: {base: 'macos*'}` or scoped `execution_needs` to request a
provider-defined environment. `--require-base` overrides the job requirement;
`--execution-base` / `C2J_EXECUTION_BASE` report the actual concrete allocation.
Matching is case-sensitive over the full name, with only `*` as a wildcard and
no aliases. Exactly `*` accepts a missing allocation base. Concrete names cannot
contain stars. Image/platform requirements are legacy-only and cannot be mixed
with base. See [execution tracking](../../../GUIDE-Execution-Tracking.md).

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

Register the target cell before submission, for example with `c2j cell create root --repo REPO`. Every cell requires explicit creation; `root` is an ordinary name:

```bash
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/my-recipe.yaml --cell root --embed
```

`--self` and `--cell` are mutually exclusive.

## Validation Before Hand-Off

For recipe edits, run the narrowest applicable validation:

```bash
go test ./pkg/recipe ./pkg/template ./pkg/worker/...
c2j submit "Run the requested task" --advanced-recipe-file ./recipes/my-recipe.yaml --run --embed
```

If the user supplied a test scenario, prefer `c2j test` with that scenario over a generic run.

For runtime cases, use `match.selector` to target an extension and `match.cell`
to distinguish foreign-cell invocations; `repeat: true` reuses a fixture.
Passthrough commands run in disposable worktrees in the worker environment.
The former `runtime.command_sandbox` override has been removed. File sources may be directories.
Use case-level `expect_error` for an expected execution error; never turn every
CLI failure into a passing negative test. CEL `calls[].artifacts` exposes small
fixture document contents for routing assertions.

Use `options.validation_mode: path_only` on mock cases whose unselected branches
require absent runtime data. The default remains whole-graph validation. Runtime
CEL assertions expose `repositories.<cell>` with the final head, added commit
count, cleanliness, changed filenames, and small changed file contents.

Use `runtime.observe_files: [candidate.txt, experiment.txt]` to inspect selected
cell-relative files in `calls[].worktree` before each invocation. Absent files
are absent map keys; regular files up to 64 KiB are included. Compare a resumed
session input with `calls[].outputs.session` from the intended earlier call;
checking only the object type does not establish correct checkpoint routing.

`--cell` accepts a name only. `--cell-repo` is a portable hosted repository assertion. Set `self.cell` explicitly in project configuration; see [named cells](https://github.com/colony-2/c2j/blob/main/GUIDE-Named-Cells.md).
