# Colony2 Recipe Authoring Notes (for agents)

This repo uses Colony2 “recipes” as workflows. When editing recipes, prefer small, incremental changes and validate + run after each change.

## Workflow Debugging (c2)
- Prefer `c2 workflow outcome <id> --output json` to check status and error details. It can return HTTP 425 while still running/retrying; poll until `status` is terminal.
- `c2 workflow output get <id>` and `c2 workflow artifact *` are only reliable once the workflow is done; don’t fetch outputs/artifacts while still running.
- Recipes can retry on failure; allow time before assuming a hang.

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

## Ticket Context Gotchas
- Depending on how a workflow was started, `context.ticket.*` may be missing/empty even when `context.actor.ticket_id` is set.
- If you need ticket data, verify what is actually populated in your run before relying on `context.ticket.title/description`.

## Ticket Reassignment
- `ticket.manage` `update_ticket` does not reliably support changing a ticket’s cell (attempts with `cell` / `cell_name` / `cell_id` may no-op).
- Current working pattern: create a new ticket in the target cell via `ticket.manage` `create_ticket`, then annotate/close or mark the original ticket as waiting.

## Recipe Publishing
- `c2 recipe update ... --publish` will fail with “no changes to commit” if the content is identical; make a real edit (even a version/desc bump) before re-publishing.
