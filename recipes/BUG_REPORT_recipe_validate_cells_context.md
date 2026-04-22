# Bug Report: `recipe validate` fails when templates use `cells()`

## Summary
`c2 recipe validate` fails for recipes that reference `{{ cells | to_json }}` (or otherwise invoke `cells()`) when run standalone, because `context.workflow.project_id` is not available during validation.

## Environment
- CLI: `c2` / `colony2` recipe validation command
- Date observed: 2026-02-22
- Working directory: `/src`

## Reproduction
1. Use a recipe containing `{{ cells | to_json }}` in templated inputs (for example, prompt text).
2. Run:
   - `c2 recipe validate --name new-ticket-triage --content-file new-ticket-triage.yaml`
   - or `c2 recipe validate --name new-ticket-requirements-planning --content-file new-ticket-requirements-planning.yaml`

## Actual Result
Validation fails with:

`cells: project_id is required in context.workflow.project_id`

## Expected Result
Either:
1. `recipe validate` provides/accepts project context so `cells()` can resolve, or
2. `recipe validate` skips runtime evaluation of `cells()` and only performs structural/schema validation, or
3. `cells()` degrades gracefully during validation (empty list / warning).

## Impact
- Recipes that are valid in real workflow execution cannot be validated standalone.
- Blocks iterative local authoring/testing for recipes that correctly use project-aware helpers.

## Notes
- These recipes execute successfully in real workflow/ticket context where `context.workflow.project_id` is set.
- The issue appears specific to standalone validation context, not runtime execution behavior.

## Suggested Fixes
1. Add `--project-id` / `--context-file` support to `recipe validate` so required context can be supplied.
2. Change `recipe validate` to avoid strict runtime resolution of helper functions requiring workflow context.
3. Return a clearer diagnostic that distinguishes “runtime context missing in validate mode” from recipe errors.
