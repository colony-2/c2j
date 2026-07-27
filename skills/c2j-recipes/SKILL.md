---
name: c2j-recipes
description: Create, edit, review, validate, test, submit, and locally run c2j recipe YAML. Use for prompts about c2j recipes, recipe files, includes, sequences, state machines, child groups, child recipe ops, artifacts, templates, CEL, jq helpers, context visibility, git state, c2ops selectors, rule gates, retries, timeouts, failure handling, and `c2j submit --recipe-file` or `c2j test` workflows.
---

# c2j Recipes

Use this skill as the main router for c2j recipe authoring work.

## Workflow

1. Read [references/recipe-files.md](references/recipe-files.md) and [references/examples.md](references/examples.md) before creating or changing recipe YAML.
2. Read [references/control-flow.md](references/control-flow.md) for sequences, state machines, child groups, child recipe ops, failure handling, retries, timeouts, and orchestration patterns.
3. Read [references/data-context-artifacts.md](references/data-context-artifacts.md) for inputs, defaults, templates, CEL, jq/JSON helpers, task context, op-visible paths, artifacts, inbox/outbox binding, and invalid data-reference fixes.
4. Read [references/ops-catalog.md](references/ops-catalog.md) before choosing ops, including command execution, sleep, extension selectors, child recipe ops, git ops, human input patterns, or test mocks.
5. Read [references/c2ops.md](references/c2ops.md) when a recipe uses `git+https://github.com/colony-2/c2ops.git//...` selectors. Use `$c2ops-extension-ops` for deeper c2ops selector work.
6. Read [references/git-state.md](references/git-state.md) for durable worktree behavior, persisted git state, `thinpackrebase`, `squashrebasemerge`, and git context propagation.
7. Read [references/testing-and-cli.md](references/testing-and-cli.md) when validating with `c2j test`, submitting with `c2j submit --run --embed`, passing inputs, attaching artifacts, or listing jobs.

Prefer current local source and fixtures over memory when details conflict. Keep generated recipe YAML small enough to validate, include explicit `id`, `version`, `input_schema`, and `outputs` when useful, and run the narrowest relevant `c2j test` or `c2j submit --recipe-file ... --run --embed` command when the user asks for execution.
