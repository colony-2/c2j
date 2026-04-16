# Spec: Fixture Framework Support for Secondary Recipes

## Goal
Allow fixture tests to load and resolve additional recipes referenced by the main recipe under test (e.g., child recipes invoked via `recipe-child` ops). This enables integration tests for recipes that orchestrate other recipes.

## Current Limitation
The fixture harness (in `/src/server/recipe-worker/test-fixtures`) registers only the primary recipe. The artifact path uses a registry that rejects unknown recipe IDs, so secondary recipe invocations fail.

## Proposed Test Schema Extension
Add an optional `recipes` list to `.test.yaml` files:

```yaml
recipes:
  - child-simple.yaml
  - child-artifact.yaml

tests:
  - name: basic_child
    inputs: {}
    want: {}
```

- Paths are relative to `recipes/`.
- The main recipe is still inferred from the test filename.

## Harness Behavior Changes
When `recipes` is present:
1. Load each listed recipe file from `recipes/`.
2. Build a registry map keyed by `recipe.id`.
3. Use this registry for recipe resolution during execution.

### Registry Requirements
- A recipe reference lookup should return the matching recipe definition by `id`.
- Unknown recipe refs should return `error: unknown recipe <id>`.
- If any recipe in `recipes` fails to load, the test should fail with a clear message.

## Execution Paths to Update
Both execution modes should resolve secondary recipes:
1. **Standalone executor path** (no artifacts).
2. **Toy engine path** (`executeRecipeWithArtifacts`).

Suggested API shape:
- Extend `RunTestOnAllRecipes` to parse `recipes` and load definitions.
- Add a helper to build a `Registry` function with the primary + secondary recipes.
- Replace the hard-coded registry in `executeRecipeWithArtifacts` with the new registry.

## Backwards Compatibility
If `recipes` is omitted, behavior must remain unchanged.

## Documentation
Update `/src/server/recipe-worker/test-fixtures/HOW_TO_ADD_RECIPE_FIXTURE_TESTS.md` with:
- The new `recipes` field.
- When to use it (child recipe tests, composition, or recipe-child ops).

## Acceptance Criteria
- Tests without `recipes` run unchanged.
- A test that invokes another recipe passes. (tests for this will be in the module that enables running secondary recipes)
- Errors are explicit for missing or unknown recipes.

