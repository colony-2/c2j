# C2J Artifact Set Bindings User Guide

Use artifact set bindings when an op needs a whole group of artifacts but the
recipe should not know every artifact name in advance.

## Bind A Whole Set

Bind an artifact map to an inbox directory with the op node's `artifacts:` map:

```yaml
sequence:
  - id: inspect
    op: command_execution
    artifacts:
      submitted/: '${{ context.artifacts }}'
    inputs:
      run: |
        find "${{ context.environment.op.inbox }}/submitted" -type f
```

Each artifact is written under the binding directory with its existing artifact
name. If `context.artifacts` contains `brief.md` and
`docs/requirements.md`, the op sees:

```text
${{ context.environment.op.inbox }}/submitted/brief.md
${{ context.environment.op.inbox }}/submitted/docs/requirements.md
```

Use `./` to bind a set directly at the inbox root:

```yaml
artifacts:
  ./: '${{ context.artifacts }}'
```

## Supported Sources

Set bindings work with any artifact set available to templates:

```yaml
artifacts:
  submitted/: '${{ context.artifacts }}'
  previous-step/: '${{ sequence.prepare.artifacts }}'
  previous-state/: '${{ states.plan.artifacts }}'
```

They also work with CEL artifact helpers:

```yaml
artifacts:
  release/: '${{ artifact_filter(sequence.build.artifacts, {"name_suffix": ".zip"}) }}'
```

## Combine With Explicit Bindings

Explicit one-file bindings still work in the same op:

```yaml
artifacts:
  submitted/: '${{ context.artifacts }}'
  canonical-brief.md: '${{ context.artifacts["brief.md"] }}'
```

The explicit binding controls one exact inbox path. The set binding expands to
one binding per artifact in the set.

## Collision Rules

If two bindings would write the same inbox path, c2j fails before running the
op. Move one set under a different directory or remove the duplicate binding:

```yaml
artifacts:
  submitted/: '${{ context.artifacts }}'
  submitted/brief.md: '${{ context.artifacts["brief.md"] }}'
```

Bindings also fail when one destination would be both a file and a directory,
such as `docs` and `docs/brief.md`.

## Notes

- Artifact set bindings are opt-in per op. Submitted artifacts are not
  automatically added to every op.
- Binding expansion only passes artifact refs to the executor. It does not read
  artifact contents during template evaluation.
- Final inbox paths must follow the same safety rules as explicit artifact
  bindings: relative paths only, with no `..` segments.
