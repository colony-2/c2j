# Bug Report: `c2j test run` panics on choice-based `input` forms before mocks execute

## Summary

`c2j test run` panics when executing an `input` op whose form includes choice `options` (`multiple_choice`, `checkboxes`, or `dropdown`). The panic happens during built-in op input validation before the test harness can return the declared mock.

## Environment

- CLI: `c2j test`
- Observed c2j module version: `v0.0.5`
- Working directory: `/src`
- Reproduced: May 13, 2026

## Reproduction

Validate the case first:

```bash
c2j test validate \
  --recipe-file ./job-merge.yaml \
  --file ./recipe-tests/job-merge.scenario.md \
  --case ts-020-cancel-skips-merge \
  --strict
```

Then run the same case. It reaches a mocked choice input:

```bash
c2j test run \
  --recipe-file ./job-merge.yaml \
  --file ./recipe-tests/job-merge.scenario.md \
  --case ts-020-cancel-skips-merge \
  --parallelism 1 \
  --artifact-mode inline \
  --out-dir /tmp/job-merge-ts020 \
  --evaluation-mode enforce
```

## Actual Result

Validation succeeds, but execution exits with a panic before the mock result is returned:

```text
panic: Duplicate param required_if=Type for required_if Options
```

The stack points at `pkg/input/activity.go` / `pkg/input/types.go` validation tags:

```text
validate:"omitempty,required_if=Type multiple_choice required_if=Type checkboxes required_if=Type dropdown,min=1,dive"
```

## Expected Result

Mocked `input` ops should return the declared mock output without validating a form through a tag configuration that can panic. If validation must run before mocks, the validation tags should not use duplicate `required_if` parameters.

## Impact

- Cases covering structured human approval/input forms compile and validate.
- Full local execution is blocked for those cases.
- Declared mocks do not avoid the failure because built-in `input` validation runs before mocked `DoTask` execution.
- `recipe-tests/run-all.sh` currently runs non-input-gated cases and leaves these cases validation-only until c2j fixes the validator tag or mock ordering.

## Suggested Fix

Replace duplicate `required_if` tags with a custom validator or a single validation rule that permits `multiple_choice`, `checkboxes`, and `dropdown` without duplicate parameter names.
