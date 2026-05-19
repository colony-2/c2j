# Bug Report: state helpers can render `null` during `c2j test validate`

## Summary

`state_exists(...)` and `state_output(..., default)` can still produce a `null`
input value during `c2j test validate` when used in a selector op input that
expects a string.

This blocks use of the new recipe authoring ergonomics helpers in places where
schema validation requires a concrete string value.

## Status

Fixed in `/c2j` commit `bf010f0` (`Fix validation fallback for safe CEL
helpers`). Verified on 2026-05-15 at 23:20:56 UTC with
`/tmp/c2j-local-bin/c2j` built from that commit: `new-ticket.yaml` no longer
fails with `/sessionId: got null, want string`.

A separate `input` semantic validation issue is still present and is tracked in
`guides/BUG_REPORT_C2J_TEST_VALIDATE_REQUIRED_INPUT_MOCKS_IGNORED.md`.

## Environment

- Date detected: 2026-05-15 at 23:01:07 UTC
- Reproduced: 2026-05-15 at 23:02:37 UTC
- c2j checkout: `/c2j`
- c2j commit: `144a1dbf` (`Add recipe diagnostics assertions`)
- Recipe: `new-ticket.yaml`
- Suite: `recipe-tests/new-ticket.scenario.md`

## Reproduction

`new-ticket.yaml` was updated to use the new state helpers for the c2ops Codex
`sessionId` input:

```yaml
implement:
  op: git+https://github.com/colony-2/c2ops.git//codex@main
  inputs:
    sessionId: >-
      ${{
        state_exists("implement")
          ? state_output("implement", "sessionId", "")
          : ""
      }}
```

`implement_resume` uses the same pattern with fallback to a prior `implement`
state:

```yaml
sessionId: >-
  ${{
    state_exists("implement_resume")
      ? state_output("implement_resume", "sessionId", "")
      : (state_exists("implement") ? state_output("implement", "sessionId", "") : "")
  }}
```

Run:

```bash
/tmp/c2j-local-bin/c2j test validate \
  --recipe-file /src/new-ticket.yaml \
  --file /src/recipe-tests/new-ticket.scenario.md \
  --parallelism 1
```

Actual output includes:

```text
ERROR failed to execute op op=git+https://github.com/colony-2/c2ops.git//codex@main err="failed to validate selector inputs: jsonschema validation failed with 'inmem://op-schema.json#'
- at '/sessionId': got null, want string"
```

All TS-031..TS-036 and TS-040..TS-041 fail this way during `test validate`.

## Expected Behavior

The expression should render a string in validation and runtime:

- Before any completed `implement` invocation, `state_exists("implement")`
  should be false, so `sessionId` should render as `""`.
- If the state exists but the output path is missing, `state_output("implement",
  "sessionId", "")` should return `""`.
- For `implement_resume`, if neither prior state exists, the whole expression
  should render as `""`.

In no case should this expression render `null`, because every authored branch
has a string fallback.

## Actual Behavior

During `c2j test validate`, the rendered selector input for `sessionId` becomes
`null`, and JSON schema validation rejects it before the op can be validated as
successful.

## Impact

- Prevents recipes from using the new state helper primitives in selector inputs
  with required string schemas.
- Forces authors back to verbose direct-state checks for schema-sensitive
  inputs.
- Undermines the ergonomics feature goal of replacing repeated
  `"state" in states && has(...)` blocks with `state_exists` and
  `state_output`.

## Suggested Fix

Ensure state helper expressions preserve authored fallback values during
semantic validation.

Regression coverage should include a selector or typed op input with:

```yaml
sessionId: >-
  ${{
    state_exists("implement")
      ? state_output("implement", "sessionId", "")
      : ""
  }}
```

`c2j test validate` should assert the rendered input value is `""`, not `null`,
when `implement` has not completed.
