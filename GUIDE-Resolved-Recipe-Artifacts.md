# Resolved recipe artifacts

Submitted recipe files already use `<id>.recipe.yaml` job-input artifacts.
Expanded source from `root-source` and the legacy `root-source-load` recovery
task now uses a task-output artifact named `_c2j/resolved-recipe.yaml`.
Deferred hook loading uses the same representation and retains its workspace
thin-pack artifacts.

New results contain `recipe_yaml_artifact: "_c2j/resolved-recipe.yaml"` and omit
`recipe_yaml`. Source kind, submitted and resolved selectors, commit, pinning,
and include provenance retain their existing meanings. `artifact_name` still
identifies the originally submitted recipe, when applicable.

The artifact is persisted with the task result through JobDB's normal API. c2j
does not construct storage keys or upload recipes to a separate store. Large
YAML therefore does not consume the 256 KiB chapter application-payload budget.
Other application fields remain subject to that limit.

## Reading old and new results

- Parse task data with `compiler.ParseResolvedRecipeSourceTaskData`, retaining
  its artifact context. This reads metadata and artifact descriptors only.
- Use `LoadRecipeContext(ctx)` for execution or `RecipeYAMLBytes(ctx)` when
  explicitly displaying source. `ParseResolvedRecipeSourceJSON` is for metadata
  only; JSON cannot resolve an artifact reader.
- Match the locator against artifact logical names, or use the reserved name
  when no locator was recorded. Use the supplied artifact reader and its actual
  stored key, including inherited keys.
- An artifact takes precedence over the old inline field. When no matching
  artifact entry exists, a nonempty legacy `recipe_yaml` remains readable.
  An advertised artifact with no entry and no inline source is an error.
  Artifact read failures, empty content, and parse failures never select inline
  source instead.
- Older metadata-only results retain the recorded `root-source-load` recovery
  path. Read-only replay requires that recovery result to exist in history;
  it does not resolve the repository or execute missing tasks.

Story detail continues to return materialized `recipe.source.recipe_yaml`.
Task output remains the original recorded value, and source display never writes
materialized YAML into history. Ordinary job listing does not fetch source
artifacts.

Task sites, command descriptors, and contract versions are unchanged. Existing
inline histories require no application-data migration. Workers and clients
that consume new source results must support artifact loading; deploy compatible
readers before routing new results to them.

Tests cover 1.3 MB root and hook recipes, the default chapter limit, interrupted
worker recovery, SQLite and remote replay, legacy inline results, artifact
precedence and failures, inherited keys, and story source rendering.
