# Recipe Authoring Guide

This is the default recipe-authoring workflow for this repo.

Author recipes locally, then validate them by submitting the recipe file directly with `c2j submit --run --embed`. That path exercises the same durable runtime model the real job uses, without requiring you to publish the recipe first.

## 1. Keep the Execution Model Straight

Recipes orchestrate ops. Ops do not run inside your interactive shell session just because you authored the YAML locally.

Treat each op as a remote durable step:

- pass structured data through `inputs`
- pass files between steps through `artifacts`
- read/write task-local files through `context.environment.worktree_path`, `context.environment.inbox`, and `context.environment.outbox`
- export anything later nodes need through the enclosing node's `outputs:`

The most common authoring mistake is assuming a later step can read a path written by an earlier step without artifact plumbing. It cannot.

Keep these references open while authoring:

- `NODE_SCOPE_SPEC.md`
- `TEMPLATE_REFERENCE_CHEATSHEET.md`
- `template_resolution.md`
- `RECIPE_ARTIFACTS_REFERENCE.md`
- `TASK_EXECUTION_CONTEXT_REFERENCE.md`

## 2. Pick the Right Op

Start with the smallest op that matches the job:

- `command_execution`: bounded shell commands against the task worktree or inbox/outbox
- `llm_inference2`: schema-constrained model calls and lightweight tool/file reasoning
- `codex.exec`: multi-step agentic coding or review work
- `gha.run` / `gha.runs`: reuse existing GitHub Actions workflows instead of rebuilding them in shell
- `recipe.run_and_get_result` / `recipes.run*`: delegate work to child recipes
- `input`: explicit human approval or structured user input
- `thinpackrebase` / `squashrebasemerge`: integrate git changes back to the base repo
- extension ops: package repo-specific behavior behind a stable op interface

Use `ops/README.md` as the entrypoint to the per-op guides.

## 3. Default Authoring Loop: `c2j submit --run --embed`

### Confirm cell resolution

`c2j submit` targets the current cell by default. That works when the repo has a usable `.c2j/config.yaml`.

Useful commands:

```bash
c2j self
c2j cells
c2j init --stdout
```

If the current directory does not resolve cleanly as a cell, either:

- create or update `.c2j/config.yaml`, or
- pass `--cell <repo-or-path>` explicitly on submit

### Submit the recipe file directly

For day-to-day authoring, prefer `--recipe-file` over publishing a named recipe first:

```bash
c2j submit \
  --recipe-file ./recipes/my-recipe.yaml \
  --run \
  --embed
```

If the recipe needs inputs:

```bash
c2j submit \
  --recipe-file ./recipes/my-recipe.yaml \
  --inputs-file ./recipes/test-inputs.yaml \
  --run \
  --embed
```

What this gives you:

- local recipe-file authoring
- submission validation
- immediate execution
- a live job story in the terminal
- no dependency on a separately managed runtime

If the job pauses and you want to continue the same submission later:

```bash
c2j exec --embed --job-id <job-id>
```

To find recent job IDs for the current cell:

```bash
c2j list --self --embed
```

## 4. Author for Remote Ops, Not for Your Laptop

Most of the friction in recipe authoring comes from forgetting that op execution is isolated and durable.

Write recipes with these rules:

- use `context.environment.worktree_path` when an op needs the repo checkout
- use `context.environment.outbox` to emit files for downstream steps
- use an `artifacts:` block only on ops that support inbox bindings
- use `${{ ... }}` for raw CEL values when a field expects a list, map, boolean, or number
- use `json_parse(...)` when consuming `llm_inference2` responses produced under `response_schema`
- guard optional values with `has(...)` before indexing or dereferencing

For `codex.exec`, pass both:

- `worktree_path: "{{ context.environment.worktree_path }}"`
- `cell_relative_path`: usually `{{ context.workflow.cell_path }}` with a fallback to `"."` when the cell path may be empty

## 5. Validation Levels

Use the fastest tool that answers the question you have:

1. `c2j submit --recipe-file ... --run --embed`
   Use this first. It is the default manual validation path for recipe authoring.
2. `c2j exec --embed --job-id ...`
   Use this when continuing an existing blocked or partially completed run.
3. `c2 recipe test ...`
   Use this only when you need curated suites, mocks, repeated cases, or artifact assertions. See `RECIPE_TESTING_CLI_USER_GUIDE.md`.

## 6. Recommended References

- authoring workflow: `RECIPE_STARTER_GUIDE.md`
- op index: `ops/README.md`
- sequence semantics: `SEQUENCE_GUIDE.md`
- state-machine semantics: `STATE_MACHINE_GUIDE.md`
- template and scope rules: `NODE_SCOPE_SPEC.md`, `TEMPLATE_REFERENCE_CHEATSHEET.md`, `template_resolution.md`
- artifacts and runtime paths: `RECIPE_ARTIFACTS_REFERENCE.md`, `TASK_EXECUTION_CONTEXT_REFERENCE.md`
