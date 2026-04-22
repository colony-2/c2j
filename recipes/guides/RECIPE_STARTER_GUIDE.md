# Recipe Starter Guide (Read This First)

What is a recipe? It’s a declarative workflow file that orchestrates ops (commands, LLM calls, git actions, child recipes, etc.) to solve a task end‑to‑end. You describe the steps, data flow, and conditions; the executor handles scoping, templating, artifact plumbing, retries, and git state so agents can perform complex, multi-step work reproducibly and safely. The usual entry point is a ticket: when a ticket is created, it launches its paired recipe, and that recipe targets a specific cell (workspace) so execution runs in the right codebase and context.

Use this short path to get productive quickly, with direct links to the deeper docs that live in `/src/guides`.

## 1) Build a working mental model
- Read `RECIPE_AUTHORING_GUIDE.md` for the end-to-end authoring flow (schema, validation, structure).
- Keep `NODE_SCOPE_SPEC.md` open to avoid reference mistakes when nesting `sequence`/`state`/`op` nodes.
- Use `TEMPLATE_REFERENCE_CHEATSHEET.md` plus `template_resolution.md` for exact templating rules (interpolation vs. raw CEL, when-conditions).

## 2) Wire data and files correctly
- `RECIPE_ARTIFACTS_REFERENCE.md` explains how inbox/outbox work, how to emit and bind artifacts, and how to thread them through retries.
- `TASK_EXECUTION_CONTEXT_REFERENCE.md` lists the `context.*` fields you can safely template (actor, ticket, env paths, workflow, git, invocation).
- `GITSTATE_REFERENCE.md` covers how git metadata flows and when to propagate `git_context_patch` outputs.

## 3) Know your ops
- The op catalog lives under `ops/`. Start with `ops/OP_COMMAND_EXECUTION.md` (general shell), `ops/OP_INPUT.md` (user prompts), `ops/OP_LLM_INFERENCE2.md` (LLM calls), and `ops/OP_CODEX_EXEC.md` (agentic execution).
- For GitHub workflow reuse: see `ops/OP_GITHUB_ACTIONS.md` (`gha.run`, `gha.runs`).
- For git history updates: see `ops/OP_THINPACKREBASE.md` and `ops/OP_SQUASHREBASEMERGE.md`.
- For child recipes: see `ops/RUN_RECIPE.md` (starting, waiting, fetching child outputs).
- For task lifecycle helpers: `ops/OP_SLEEP.md`, `ops/EXTENSION_OPS.md` (extension runners), and `ops/TICKET_MANAGE_OP.md`.

## 4) Author safely and validate early
- Generate/validate against the schema exactly as described in `RECIPE_AUTHORING_GUIDE.md` before running anything.
- Use `${{ ... }}` for CEL expressions and `{{ ... }}` for Go templates in string fields; use CEL single-expression form for raw non-scalars (maps/lists) and either CEL or standalone simple Go paths for scalar types (see cheatsheet).
- Always export data via an `outputs:` block when it must cross a scope boundary (sequence ↔ parent, state ↔ parent).

## 5) Test and iterate
- Add regression coverage using `HOW_TO_ADD_RECIPE_FIXTURE_TESTS.md`; it explains the fixture layout and harness.
- If your recipe launches children, test them independently first, then exercise the parent with `ops/RUN_RECIPE.md` patterns.

## 6) Handy quick references
- Node visibility rules at a glance: `NODE_SCOPE_SPEC.md`.
- Common template patterns and pitfalls: `TEMPLATE_REFERENCE_CHEATSHEET.md`.
- Artifact plumbing examples (outbox → inbox): `RECIPE_ARTIFACTS_REFERENCE.md`.
- Context fields you can rely on (paths, ticket metadata): `TASK_EXECUTION_CONTEXT_REFERENCE.md`.
- Git propagation do’s/don’ts: `GITSTATE_REFERENCE.md`.

## 7) Minimal day-one checklist
1. Skim `RECIPE_AUTHORING_GUIDE.md` and `TEMPLATE_REFERENCE_CHEATSHEET.md`.
2. Outline your node structure; mark which values must be bubbled up via `outputs:`.
3. Pick ops from `ops/` references; copy their required inputs/output shapes.
4. Wire templates following `template_resolution.md`; validate with `go run ./cmd/cortex validate path/to/recipe.yaml`.
5. Add a fixture test (`HOW_TO_ADD_RECIPE_FIXTURE_TESTS.md`) before submitting.

Keep this guide nearby; follow the linked docs whenever you’re unsure about scope, templating, artifacts, git propagation, or op semantics.
