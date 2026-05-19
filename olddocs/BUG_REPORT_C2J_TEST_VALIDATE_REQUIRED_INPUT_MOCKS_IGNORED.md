# Bug Report: `c2j test validate` ignores input mocks for required form fields

## Summary

`c2j test validate` still fails recipe cases that pass under `c2j test run`
when the recipe contains required `input` form fields without explicit defaults.

The validator appears to execute the recipe in `ValidateAll` mode without using
the case's mocked `input` outputs. That causes the input output normalizer to
see an empty output and fail with:

```text
required input field "decision" missing from output
```

This makes valid recipe tests impossible to validate without adding artificial
defaults to human review gates.

## Environment

- Date detected: 2026-05-15 at 23:18:39 UTC
- Reproduced: 2026-05-15 at 23:20:56 UTC
- c2j checkout: `/c2j`
- c2j commit: `bf010f0` (`Fix validation fallback for safe CEL helpers`)
- Recipe: `new-ticket.yaml`
- Suite: `recipe-tests/new-ticket.scenario.md`

## Reproduction

Run:

```bash
/tmp/c2j-local-bin/c2j test validate \
  --recipe-file /src/new-ticket.yaml \
  --file /src/recipe-tests/new-ticket.scenario.md \
  --parallelism 1
```

The suite fails every case with the same semantic validation error. Example:

```text
2026/05/15 23:19:07 INFO executing op op=input
2026/05/15 23:19:07 ERROR failed to execute op op=input err="required input field \"decision\" missing from output"
ts-034-merge-success-completes-job invalid
Error: 8 case(s) invalid or errored
```

The same suite passes under `test run`:

```bash
/tmp/c2j-local-bin/c2j test run \
  --recipe-file /src/new-ticket.yaml \
  --file /src/recipe-tests/new-ticket.scenario.md \
  --parallelism 1 \
  --artifact-mode inline \
  --out-dir /tmp/new-ticket-run-current \
  --evaluation-mode enforce
```

All 8 cases pass.

## Example Form

`new-ticket.yaml` has required human review fields such as:

```yaml
ready_to_merge_review:
  op: input
  transitions:
    switch: outputs.fields.decision
  inputs:
    form:
      title: "Ready-to-merge review"
      fields:
        - id: decision
          type: multiple_choice
          required: true
          options:
            - value: merge
            - value: revise_current_stage
            - value: cancel_job
```

The relevant test cases provide mocked outputs for the input states they execute:

```yaml
- match:
    node_path: new-ticket/ready_to_merge_review/input
  behavior:
    mode: return
    outputs:
      fields:
        decision: merge
        feedback: ""
        upstream_repo: ""
        upstream_branch: ""
        commit_message: ""
```

`c2j test run` uses these mocks correctly. `c2j test validate` appears not to.

## Expected Behavior

`c2j test validate` should validate recipe execution semantics without forcing
authors to add behavior-changing defaults to required human-review fields.

Acceptable fixes would include one of:

- Apply the recipe case's op mocks during semantic validation.
- When validating all paths, synthesize explicit branch values for required
  choice fields based on the rendered form options.
- Treat unsubmitted required input fields as a validation boundary during
  semantic validation instead of an invalid recipe, while still failing
  recipe-test mocks that omit required fields on paths they actually mock.

The stricter mock behavior should remain: a mocked successful `input` output
that omits a required field should fail.

## Actual Behavior

`c2j test validate` executes an `input` op with no submitted fields and no case
mock output, then the input normalizer correctly rejects the empty required
field:

```text
required input field "decision" missing from output
```

This is correct for an incomplete mock, but not for semantic validation that is
not applying the case's mocks.

## Impact

- Blocks `c2j test validate` for recipes with required human review gates.
- Encourages recipe authors to add unsafe or misleading defaults to approval
  fields only to satisfy the validator.
- Undermines the new switch/table transition and input-output-default ergonomics
  because common human review states become validator-hostile.
