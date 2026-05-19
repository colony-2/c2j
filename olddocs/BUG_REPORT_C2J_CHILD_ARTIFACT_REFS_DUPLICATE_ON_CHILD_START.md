# Bug Report: Child Recipe Artifact Refs Duplicate Submitted Artifacts

Detected: 2026-05-19

## Summary

When a parent recipe passes a submitted artifact to a child recipe through
`recipe.run_and_get_result.inputs.artifacts`, the child job can fail before
execution with a duplicate submitted artifact error.

The parent passes artifact refs like:

```yaml
artifacts: '${{ context.artifacts.map(k, context.artifacts[k]) }}'
```

The child binds those artifacts with:

```yaml
artifacts:
  submitted/: '${{ context.artifacts }}'
```

Expected behavior: the child job receives one submitted artifact ref named
`brief.md`, and the child op can materialize it at
`{{ context.environment.op.inbox }}/submitted/brief.md`.

Actual behavior: child job startup reports two submitted artifacts with the
same name:

```text
duplicate submitted artifact name "brief.md" refers to both
stored:<parent-job>:0:brief.md and stored:<child-job>:0:brief.md
```

## Impact

Recipes cannot currently verify or rely on parent-to-child artifact forwarding
for submitted artifacts. This blocks `new-ticket` from safely forwarding
submitted files to child planning recipes and having those children bind the
files through `context.artifacts`.

Current `/src/recipe-tests` scenario coverage does not catch this because the
`new-ticket` child recipe calls are mocked as `recipe.run_and_get_result`
outputs. Those mocks validate orchestration decisions, but they do not inspect
the rendered child start request and do not execute the child job with its
received artifacts.

## Minimal Reproduction

Create a repo with two cell recipes:

```yaml
# .c2j/recipes/parent-artifact-forwarding.yaml
id: parent-artifact-forwarding
version: "1.0"
sequence:
  - id: child
    op: recipe.run_and_get_result
    inputs:
      name: child-artifact-forwarding
      inputs: {}
      artifacts: '${{ context.artifacts.map(k, context.artifacts[k]) }}'
outputs:
  child_received: "${{ sequence.child.outputs.outputs.received }}"
```

```yaml
# .c2j/recipes/child-artifact-forwarding.yaml
id: child-artifact-forwarding
version: "1.0"
sequence:
  - id: verify
    op: command_execution
    artifacts:
      submitted/: '${{ context.artifacts }}'
    inputs:
      timeout: 10s
      run: |
        set -euo pipefail
        test -f "{{ context.environment.op.inbox }}/submitted/brief.md"
        grep -q "child-artifact-forwarding-ok" "{{ context.environment.op.inbox }}/submitted/brief.md"
outputs:
  received: true
```

Run:

```bash
printf 'child-artifact-forwarding-ok\n' > /tmp/brief.md
c2j submit --cell "$REPO" \
  --recipe parent-artifact-forwarding \
  --artifact /tmp/brief.md \
  --embed \
  --tenant-id recipe-tests \
  --json
```

Execute the parent until it reports `wait_for=<child-job-id>`, execute the child
job, then continue the parent. The child fails while indexing submitted
artifacts.

## Likely Cause

`recipe.run_and_get_result` appears to start the child with both:

- concrete SWF `Artifacts` resolved from the refs
- `ArtifactRefs` containing the same refs

The child job's submitted artifact indexing then sees the same logical artifact
name from both sources and treats it as a conflict.

## Requirements

- Passing stored artifact refs to `recipe.run_and_get_result` or `recipes.run*`
  must not create duplicate submitted artifact names in the child job.
- A child job should expose each passed artifact once in `context.artifacts`.
- A child recipe must be able to bind the received artifact set with
  `submitted/: '${{ context.artifacts }}'`.
- Duplicate detection should still reject true conflicts where two distinct
  user-provided artifacts with the same child-visible name are passed.
- Add an integration test where a parent receives a submitted artifact, forwards
  it to a child recipe, and the child command op reads it from its inbox.
