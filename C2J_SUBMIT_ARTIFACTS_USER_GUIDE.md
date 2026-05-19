# C2J Submit Artifacts User Guide

Use submit artifacts when a recipe needs one or more local files at run time,
such as markdown briefs, requirements docs, logs, or config snippets.

## Attach Files

Attach files with repeatable `--artifact` flags:

```bash
c2j submit \
  --recipe-file ./recipes/review-docs.yaml \
  --artifact ./docs/brief.md \
  --artifact requirements=./docs/requirements.md \
  --run \
  --embed
```

`--artifact <path>` uses the file basename as the artifact name. In the example
above, `./docs/brief.md` is named `brief.md`.

`--artifact <name>=<path>` sets the artifact name explicitly. In the example
above, `./docs/requirements.md` is named `requirements`.

## Use Files In Recipes

Submitted files are available to templates as `context.artifacts`.

Bind the submitted artifact into an op's inbox with the op node's `artifacts:`
map:

```yaml
id: review-docs
version: "1.0"
sequence:
  - id: inspect
    op: command_execution
    artifacts:
      brief.md: '${{ context.artifacts["brief.md"] }}'
      requirements.md: '${{ context.artifacts["requirements"] }}'
    inputs:
      run: |
        cat "${{ context.environment.op.inbox }}/brief.md"
        cat "${{ context.environment.op.inbox }}/requirements.md"
```

The key under `artifacts:` controls the path inside the op inbox. The
`context.artifacts[...]` lookup selects which submitted artifact to materialize.

## Rules

- Artifact names must be unique.
- Artifact names must be relative slash-separated paths.
- Directories are not supported yet; attach regular files only.
- Files are read when the job is submitted. Later local edits do not affect the
  submitted job.
- Artifacts are not automatically added to every op. Recipes must explicitly
  bind each submitted artifact into the ops that need it.

## Common Errors

If a recipe references a missing submitted artifact, template resolution fails.
Check that the name in `context.artifacts["..."]` matches the submit name.

If an op rejects `artifacts:`, that op does not accept artifact inbox bindings.
Use an artifact-aware op such as `command_execution`, or update the op.
