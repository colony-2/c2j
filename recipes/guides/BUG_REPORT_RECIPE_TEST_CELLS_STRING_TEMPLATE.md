# Bug Report: `c2 recipe test run` fails on `{{ cells | to_json }}`

## Summary

`c2 recipe test run` fails at runtime when a recipe input string template uses `{{ cells | to_json }}`.
The same case validates successfully via `c2 recipe test validate`.

## Environment

- CLI: `c2 recipe test`
- Recipe target mode: `--recipe-file`
- Reproduced on: February 22, 2026

## Reproduction

1) Create minimal suite:

```yaml
cases:
  - id: smoke
    type: recipe_case
    inputs:
      title: test
      description: test description
```

2) Validate:

```bash
c2 recipe test validate \
  --recipe-file new-ticket-triage.yaml \
  --file /tmp/bug-cells-suite.yaml \
  --parallelism 1
```

Observed:

- `smoke valid`

3) Run:

```bash
c2 recipe test case run \
  --recipe-file new-ticket-triage.yaml \
  --file /tmp/bug-cells-suite.yaml \
  --case-id smoke \
  --out-dir /tmp/bug-cells-run \
  --artifact-mode inline \
  --evaluation-mode report_only
```

Observed result (`/tmp/bug-cells-run/cases/smoke/result.json`):

- `failure_category`: `runtime_error`
- `failure_reason`:
  - `sequence node 0 failed: failed to resolve templates op inputs: failed to resolve input 'prompt': template: string:8: function "cells" not defined`

## Expected Behavior

- `c2 recipe test case run` should resolve `{{ cells | to_json }}` the same way runtime recipes do.
- If unsupported, `validate` should fail early with a deterministic compile/validation error instead of passing.

## Actual Behavior

- `validate` passes.
- `run` fails at template resolution with `function "cells" not defined`.

## Impact

- Any recipe test case execution that touches nodes with `{{ cells | to_json }}` fails.
- Blocks behavior verification for triage/requirements workflows that depend on cell-list context.

## Notes

- This repro uses `new-ticket-triage.yaml`, but issue likely affects any recipe using `cells` in Go string templates during recipe test execution.
