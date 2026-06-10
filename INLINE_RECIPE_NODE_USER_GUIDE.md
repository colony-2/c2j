# Inline Recipe Include User Guide

`include` is an authoring-time recipe node for composing one recipe into another without starting a child job.

Use it when a workflow should be split into reusable recipe files, but execution should still behave like one expanded recipe.

## Basic Shape

```yaml
id: parent
version: "1.0"
input_schema:
  prompt:
    type: string
    required: true
inputs:
  prompt: "${{ inputs.prompt }}"

sequence:
  - id: brainstorm
    include: ./brainstorm.yaml
    inputs:
      prompt: "${{ inputs.prompt }}"

outputs:
  ideas: "${{ sequence.brainstorm.outputs.ideas }}"
```

The included recipe is a normal recipe file:

```yaml
id: brainstorm_recipe
version: "1.0"
input_schema:
  prompt:
    type: string
    required: true
  model:
    type: string
    default_value: tiny
inputs:
  prompt: "${{ inputs.prompt }}"
  model: "${{ inputs.model }}"

sequence:
  - id: draft
    op: ./tools/ops/brainstorm
    inputs:
      prompt: "${{ inputs.prompt }}"
      model: "${{ inputs.model }}"

outputs:
  ideas: "${{ sequence.draft.outputs.ideas }}"
```

At submit or root-source resolution time, `include` is expanded into ordinary recipe nodes. The executor does not run a special include node.

## Syntax

Use either scalar shorthand:

```yaml
- id: verify
  include: ./verify.yaml
  inputs:
    plan: "${{ sequence.plan.outputs.plan }}"
```

Or object form:

```yaml
- id: verify
  include:
    recipe: ./verify.yaml
  inputs:
    plan: "${{ sequence.plan.outputs.plan }}"
```

The callsite uses normal node metadata:

- `id`: output key in the parent `sequence` or `states` map.
- `inputs`: raw inputs passed to the included recipe input schema.
- `when`, `vars`, `timeout`, `retry`, `catch`, `const`: normal node behavior on the generated include boundary.

Do not put `inputs` under `include`. The include object only identifies the recipe reference.

## Recipe References

`include.recipe` uses the same recipe reference vocabulary as recipe submission:

- Local file references such as `./phase.yaml` and `../shared/check.yaml`.
- Git recipe selectors such as `git+https://github.com/acme/cell.git//.c2j/recipes/check.yaml@main`.
- Server/cell recipe refs when the configured recipe resolver supports them.

Relative local file includes are resolved relative to the including recipe file.

Relative includes inside a git-sourced recipe are resolved relative to the including recipe's path in that git repository and pinned to the same resolved commit.

## Inputs And Defaults

The included recipe's `input_schema` is enforced at the include boundary.

```yaml
- id: analyze
  include: ./analyze.yaml
  inputs:
    prompt: "${{ inputs.prompt }}"
```

If `./analyze.yaml` has defaults in `input_schema`, those defaults are applied before the included recipe body runs. If a required field is missing or an unknown field is passed, execution fails the same way root recipe input validation fails.

This validation is implemented as internal metadata on the expanded wrapper node. Recipe authors cannot create that internal metadata directly.

## Outputs

The included recipe must declare explicit `outputs`. The include callsite exposes those outputs under the callsite id.

```yaml
sequence:
  - id: analyze
    include: ./analyze.yaml
    inputs:
      prompt: "${{ inputs.prompt }}"

outputs:
  summary: "${{ sequence.analyze.outputs.summary }}"
```

Included recipe roots currently need to be `sequence` or `state` recipes. If you want to include a root `op` or root `child_group` recipe, wrap it in a sequence recipe and declare outputs.

## State Machines

Includes can appear inside states. The state name is still the parent-visible state key.

```yaml
state:
  initial: brainstorm
  states:
    brainstorm:
      include: ./brainstorm.yaml
      inputs:
        prompt: "${{ inputs.prompt }}"
      transitions:
        - to: review
          when: outputs.ready

    review:
      include: ./review.yaml
      inputs:
        ideas: "${{ states.brainstorm.outputs.ideas }}"
```

Inside a state transition, `outputs` refers to that included recipe's outputs after expansion.

## Snapshot Consistency

Inline recipe resolution is snapshot consistent.

During a single resolution pass:

- Every include is resolved before execution starts.
- The expanded recipe snapshot is what the job executes and replays.
- The same git repository and submitted ref resolve to one commit, even when two different include nodes reference different recipe paths in that repository.
- Relative git includes inherit the already-resolved commit from their parent recipe.

The expanded snapshot records the source kind, submitted selector, resolved selector, resolved commit, and content hash for each include boundary.

## Reporting And Tracing

Each include expands to a structural wrapper subtree. The wrapper carries internal inline metadata, and descendants inherit an `inline_stack` in execution context.

Ops can inspect the active include chain through:

```yaml
"${{ context.inline_stack }}"
```

Story and replay reporting also expose `inline_stack` on story nodes. This lets UIs show which included recipe subtree is currently executing without relying on path prefix matching.

Each stack frame includes:

- `callsite_path`
- `boundary_node_path`
- `recipe_id`
- `recipe_version`
- `source_kind`
- `submitted_selector`
- `resolved_selector`
- `resolved_commit`
- `content_sha256`

## Local Development

Local recipe file submission resolves includes before embedding the recipe in the job:

```bash
c2j submit --recipe-file ./recipes/parent.yaml --run --embed
```

Recipe tests also resolve includes and hash the expanded snapshot:

```bash
c2j test ./recipe-test.yaml --embed
```

## Limitations

- `include` is not valid as a root recipe node.
- Included root recipes must currently be `sequence` or `state` roots with explicit `outputs`.
- Runtime execution fails clearly if an unresolved include reaches the executor, which indicates a missing resolution step in a caller.
