# Bug Report: Artifact-key bindings are resolved too eagerly

## Summary

Artifact handoff between recipe nodes is blocked because artifact-key expressions are resolved as if artifact maps are already concrete at validate/runtime bind time.

Observed blocking errors:

- `no such key: requirements/plan.json`
- `artifact key is only available for artifacts that have been persisted`

This currently prevents clean artifact-first patterns (outbox → artifact key → inbox binding) in some flows.

## Environment

- CLI: `c2` / `colony2`
- Date observed: February 22, 2026
- Working directory: `/src`

## Reproduction A (minimal)

Use a two-step recipe where step 1 writes an outbox artifact and step 2 binds it into `artifacts:`.

```yaml
id: test-cmd-artifacts
version: 0.1.0
sequence:
  - id: write
    op: command_execution
    inputs:
      run: |
        set -e
        OUT="{{ context.environment.outbox }}"
        echo hi > "$OUT/foo.txt"
  - id: read
    op: command_execution
    artifacts:
      foo.txt: '${{ sequence.write.artifacts["foo.txt"] }}'
    inputs:
      working_directory: "{{ context.environment.inbox }}"
      run: "cat foo.txt"
```

Run:

```bash
c2 recipe validate --name test-cmd-artifacts --content-file /tmp/test-cmd-artifacts.yaml
```

Actual:

- `sequence node 1 failed ... no such key: foo.txt`

## Reproduction B (real recipe)

`new-ticket-requirements-planning.yaml` has:

```yaml
artifacts:
  plan.json: '${{ sequence.persist_plan_and_docs.artifacts["requirements/plan.json"] }}'
```

Run:

```bash
c2 recipe validate --name new-ticket-requirements-planning --content-file new-ticket-requirements-planning.yaml
```

Actual:

- `sequence node 1 failed ... no such key: requirements/plan.json`

## Reproduction C (recipe test run with mocks)

When the producer node is mocked with artifacts, `c2 recipe test run` still fails at bind time for the consumer node:

- `failed to resolve artifact key: artifact key is only available for artifacts that have been persisted`

## Expected Behavior

1. `recipe validate` should type-check artifact references without requiring concrete producer artifact keys to exist at validation time.
2. Runtime should resolve artifact-key references after producer completion and allow downstream `artifacts:` binding.
3. Recipe-test mocks that provide producer artifacts should yield resolvable artifact keys for downstream binds.

## Actual Behavior

Artifact-key expressions are treated as eagerly-resolved concrete map lookups, causing downstream artifact binding to fail before normal producer→consumer handoff can complete.

## Impact

- Blocks artifact-driven orchestration patterns described in artifact guides.
- Forces fallback to less-desirable patterns (value-string plumbing via summaries/adapter steps).
- Prevents verifying artifact contract behavior in recipe tests for affected flows.

## Suggested Fixes

1. Resolve `sequence.<node>.artifacts["name"]` lazily as artifact-key references in validation/runtime binding.
2. In `recipe validate`, perform structural/type validation for artifact expressions, not concrete key existence checks.
3. In recipe-test execution, ensure mocked artifacts are materialized as persisted artifact keys before downstream bind resolution.
