# Bug Report: `codex.exec` cannot receive bound artifacts in recipes

## Summary

When a recipe defines an `artifacts:` block on a `codex.exec` node, recipe validation fails with:

- `operation codex.exec does not accept artifacts`

This blocks codex-to-codex artifact handoff patterns.

## Reproduction

Given a sequence where:

1. Step A writes an artifact to outbox (for example `requirements/plan.json`).
2. Step B is `op: codex.exec` with:

```yaml
artifacts:
  plan.json: '${{ sequence.step_a.artifacts["requirements/plan.json"] }}'
```

Run:

```bash
c2 recipe validate --name <recipe-name> --content-file <recipe-file>.yaml
```

Observed:

- Validation error: `operation codex.exec does not accept artifacts`

## Expected

- `codex.exec` should support artifact inbox bindings like other ops that consume files.
- This enables multi-step codex workflows where one codex session writes artifacts and another consumes them.

## Impact

- Forces fallback patterns:
  - pass large JSON through output values, or
  - add adapter command steps to read and re-inject content.
- Reduces clean artifact-driven orchestration in recipe state/sequence design.
