# Recipe Starter Guide

Read this first if you are new to recipe authoring in this repo.

## 1. Start with the right docs

- `RECIPE_AUTHORING_GUIDE.md`
  Primary guide. Use this for the actual authoring loop and the `c2j submit --run --embed` workflow.
- `ops/README.md`
  Use this to pick the right op before reading the op-specific guide.
- `NODE_SCOPE_SPEC.md`
  Keep this open while wiring nested `sequence` and `state` references.
- `TEMPLATE_REFERENCE_CHEATSHEET.md` and `template_resolution.md`
  Use these for exact CEL and interpolation rules.

## 2. Know how files and context move

- `RECIPE_ARTIFACTS_REFERENCE.md`
  Explains inbox/outbox behavior, artifact bindings, and cross-step file handoff.
- `TASK_EXECUTION_CONTEXT_REFERENCE.md`
  Lists the `context.*` fields available at runtime.
- `GITSTATE_REFERENCE.md`
  Explains how git context propagates between ops.

## 3. Know your op families

- shell and glue: `ops/OP_COMMAND_EXECUTION.md`
- model calls: `ops/OP_LLM_INFERENCE2.md`
- agentic execution: `ops/OP_CODEX_EXEC.md`
- human input: `ops/OP_INPUT.md`
- child recipes: `ops/RUN_RECIPE.md`
- GitHub Actions reuse: `ops/OP_GITHUB_ACTIONS.md`
- repo-specific extensions: `ops/EXTENSION_OPS_GUIDE.md`
- git integration: `ops/OP_THINPACKREBASE.md`, `ops/OP_SQUASHREBASEMERGE.md`

## 4. Use the default validation loop

For almost all manual authoring work, validate by submitting the local recipe file directly:

```bash
c2j submit \
  --recipe-file ./recipes/my-recipe.yaml \
  --run \
  --embed
```

Add `--inputs-file` when needed. Use `c2j exec --embed --job-id <job-id>` to continue an existing run, and `c2j list --self --embed` to find recent jobs.

## 5. When to use the test harness

Use `RECIPE_TESTING_CLI_USER_GUIDE.md` when you need:

- repeatable multi-case suites
- mocks for ops or child recipes
- artifact assertions
- regression coverage beyond a single manual run

For everyday iteration, start with `c2j submit --run --embed`.

## 6. Minimal checklist

1. Skim `RECIPE_AUTHORING_GUIDE.md`.
2. Pick the op using `ops/README.md`.
3. Wire references with `NODE_SCOPE_SPEC.md` and `TEMPLATE_REFERENCE_CHEATSHEET.md`.
4. Validate with `c2j submit --recipe-file ... --run --embed`.
5. Add a targeted `c2 recipe test` suite only when the workflow needs durable regression coverage.
