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

## Bind All Submitted Files

When an op should receive every submitted artifact, bind the artifact map to an
inbox directory:

```yaml
sequence:
  - id: inspect
    op: command_execution
    artifacts:
      ./: '${{ context.artifacts }}'
    inputs:
      run: |
        find "${{ context.environment.op.inbox }}" -type f
```

`./` writes each artifact at its submitted name under the inbox root. For
example, submitted artifacts named `brief.md` and `docs/requirements.md` become:

```text
${{ context.environment.op.inbox }}/brief.md
${{ context.environment.op.inbox }}/docs/requirements.md
```

Use a directory prefix when you want to keep a whole submitted set grouped:

```yaml
artifacts:
  submitted/: '${{ context.artifacts }}'
```

The same two artifacts become:

```text
${{ context.environment.op.inbox }}/submitted/brief.md
${{ context.environment.op.inbox }}/submitted/docs/requirements.md
```

Whole-set bindings can be combined with explicit bindings:

```yaml
artifacts:
  submitted/: '${{ context.artifacts }}'
  canonical-brief.md: '${{ context.artifacts["brief.md"] }}'
```

If two bindings would write the same inbox path, c2j fails before the op runs
with a duplicate artifact binding error.

For the same pattern with step or state output artifacts, see
`C2J_ARTIFACT_SET_BINDINGS_USER_GUIDE.md`.

## Rules

- Artifact names must be unique.
- Artifact names must be relative slash-separated paths.
- Directories are not supported yet; attach regular files only.
- Files are read when the job is submitted. Later local edits do not affect the
  submitted job.
- Artifacts are not automatically added to every op. Recipes must explicitly
  bind each submitted artifact into the ops that need it.
- Binding a whole artifact map preserves each artifact's name under the binding
  directory.

## Common Errors

If a recipe references a missing submitted artifact, template resolution fails.
Check that the name in `context.artifacts["..."]` matches the submit name.

If a whole-set binding and an explicit binding target the same inbox path, move
one set under a different directory prefix or remove the duplicate explicit
binding.

If an op rejects `artifacts:`, that op does not accept artifact inbox bindings.
Use an artifact-aware op such as `command_execution`, or update the op.
