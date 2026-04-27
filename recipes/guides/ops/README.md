# Ops Guide

Use this guide to choose the right op before you drop into the per-op references.

## Core Rule

Most ops now run as remote durable task steps. Author them as if each step is isolated:

- send structured values through `inputs`
- move files through `artifacts`
- use `context.environment.worktree_path`, `context.environment.inbox`, and `context.environment.outbox`
- export downstream data through `outputs:`

Do not rely on ambient shell state, ad hoc temp files, or the assumption that one op can see another op's filesystem changes unless those changes are in the managed worktree or emitted as artifacts.

## Which Op To Use

- `command_execution`
  Use for bounded shell commands, light glue, validation commands, or file generation in the worktree/outbox.
  Reference: `OP_COMMAND_EXECUTION.md`

- `llm_inference2`
  Use for one model call with optional structured output, file context, and tool execution.
  Reference: `OP_LLM_INFERENCE2.md`

- `codex.exec`
  Use for agentic coding or review work that needs a repo worktree and durable progress.
  Reference: `OP_CODEX_EXEC.md`

- `gha.run`, `gha.runs`
  Use when the repository already has a workflow you should reuse. These runs are disposable and do not write back into recipe git state.
  Reference: `OP_GITHUB_ACTIONS.md`

- `recipe.run_and_get_result`, `recipes.run`, `recipes.run_and_wait`, `recipe.await_result`, `recipe.get_result`
  Use to fan work out into child recipes and consume their outputs/artifacts explicitly.
  Reference: `RUN_RECIPE.md`

- `input`
  Use for human approval, structured user answers, or workflow checkpoints that must pause for input.
  Reference: `OP_INPUT.md`

- `sleep`
  Use for explicit waits or retry backoff in orchestration flows.
  Reference: `OP_SLEEP.md`

- `thinpackrebase`, `squashrebasemerge`
  Use when the workflow is ready to integrate git changes back onto the latest upstream state.
  References: `OP_THINPACKREBASE.md`, `OP_SQUASHREBASEMERGE.md`

- selector-based extension ops
  Use when the repo needs custom packaged behavior behind a stable op interface.
  Reference: `EXTENSION_OPS_GUIDE.md`

## Selection Heuristics

- Prefer a purpose-built op over `command_execution` when one exists.
- Prefer `gha.run` or `gha.runs` over rebuilding an existing workflow in shell.
- Prefer child recipes when a phase is substantial enough to deserve its own durable workflow boundary.
- Prefer `llm_inference2` for single-turn structured reasoning and `codex.exec` for multi-step codebase work.
- Prefer artifacts for handoff data instead of parsing free-form summaries.

## Authoring Loop

When you are trying a new op or adjusting inputs, use the local validation loop:

```bash
c2j submit \
  --recipe-file ./recipes/my-recipe.yaml \
  --run \
  --embed
```

That is the fastest way to validate the authored YAML against the runtime behavior of the selected ops.
