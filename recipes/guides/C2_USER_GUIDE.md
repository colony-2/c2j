# Recipe CLI Guide

Use `c2j` for day-to-day recipe authoring in this repo.

Use `c2` only when you need the server-managed Colony2 APIs for published recipes, tickets, or historical workflow inspection.

## Recommended Authoring Loop: `c2j`

### Inspect current cell context

- `c2j self`
- `c2j cells`
- `c2j init --stdout`

`c2j submit` targets the current cell by default. If that does not resolve cleanly, create `.c2j/config.yaml` or pass `--cell <repo-or-path>`.

### Submit and run a local recipe file

```bash
c2j submit \
  --recipe-file ./recipes/my-recipe.yaml \
  --run \
  --embed
```

With inputs:

```bash
c2j submit \
  --recipe-file ./recipes/my-recipe.yaml \
  --inputs-file ./recipes/test-inputs.yaml \
  --run \
  --embed
```

Useful follow-ups:

- `c2j list --self --embed`
- `c2j exec --embed --job-id <job-id>`

Practical notes:

- `--embed` uses the embedded SWF runtime and is the fastest local feedback loop.
- `--recipe-file` is the right choice while authoring because it does not require publishing first.
- `--recipe` can target a named recipe or selector when you do want runtime resolution.

## When To Use `c2`

The older `c2` CLI is still useful for server-managed objects:

- recipes stored and published in Colony2
- tickets
- workflow history and remote workflow outputs/artifacts

Common command groups:

- projects: `c2 project list|create|update|delete`
- tickets: `c2 ticket list|create`
- recipes: `c2 recipe list|get|info|create|update|validate`
- workflows: `c2 workflow list|run|output|outcome|artifact|story`
- input requests: `c2 input-request list|respond`
- cells: `c2 cell list|create|update`

If you are editing YAML under `/src/recipes`, start with `c2j`, not `c2`.
