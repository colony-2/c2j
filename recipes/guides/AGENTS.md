# Colony2 Recipe Authoring Notes (for agents)

This repo uses Colony2 recipes as workflows. When editing recipes, prefer small, incremental changes and validate after each change with the local `c2j` loop.

## Default Workflow Debugging (`c2j`)
- Prefer `c2j submit --recipe-file ... --run --embed` while authoring. It validates the local yaml and runs it through the embedded runtime without requiring a publish step.
- Use `c2j self` to confirm current-cell resolution before assuming the recipe is targeting the right repo.
- Use `c2j list --self --embed` to find recent jobs for the current cell.
- Use `c2j exec --embed --job-id <id>` to continue a submitted job.
- If the repo has no usable `.c2j/config.yaml`, pass `--cell <repo-or-path>` explicitly on submit.

## Remote-Op Rules
- Most ops behave like remote durable steps, not like commands sharing your interactive shell state.
- Use `context.environment.worktree_path`, `context.environment.inbox`, and `context.environment.outbox` instead of ad hoc paths.
- Move files between steps with `artifacts`, not implicit filesystem assumptions.
- Export anything outer scopes need with `outputs:`.

## Template/Scope Rules (CEL + interpolation)
- State machines expose completed state outputs at `states.<state>.outputs` (root-level `outputs:` can read from there).
- Inside a state’s `transitions.when`, reference the *current state’s* op outputs via `outputs...` (not `states.<state>...`).
  - Example: for `recipe.run_and_get_result`, the child recipe outputs map is `outputs.outputs` (so a transition can use `outputs.outputs.foo`).
- Use `has(x)` / `size(list)` guards when reading optional keys or indexing lists.

## llm_inference2 + response_schema
- With `response_schema`, `sequence.<id>.outputs.response` is a **stringified JSON object**.
- Consume it with `json_parse(sequence.<id>.outputs.response).field` (no `jq`, no extra string wrapping).

## cells()
- `cells()` returns a list of maps (all string fields): `name`, `id`, `path`, `description`.
- If an LLM recommends a cell, validate it in outputs/templates:
  - `cells().exists(c, c.name == recommended_cell)`
- In prompt text, prefer Go templates: `{{ cells | to_json }}`.
- Prefer including `cells()` in LLM prompts and instruct the model to choose exactly from the provided `name` values.

## Job Context
- `context.workflow.job_id` is the stable identifier for the current job.
- Use recipe inputs and artifacts as the source of job content.

## Cross-Cell Handoff
- To hand work to another cell, start child jobs with `recipes.run` / `recipe.run_and_get_result`.
- If a workflow needs to stop after spawning cross-cell work, route into an explicit wait/hold state instead of trying to mutate a separate ticket record.

## Recipe Publishing
- `c2 recipe update ... --publish` will fail with “no changes to commit” if the content is identical; make a real edit (even a version/desc bump) before re-publishing.
