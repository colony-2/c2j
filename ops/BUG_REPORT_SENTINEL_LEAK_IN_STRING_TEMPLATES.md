# Bug Report: Sentinel Values Leak in Runtime String Inputs

## Summary
Runtime sentinel placeholders (for example `__C2_SENTINEL_ARTIFACT_INBOX__`) can leak into op inputs when the sentinel appears as part of a larger string value (such as a file path with a suffix).

This causes runtime command/prompt failures even though template resolution succeeded.

## Environment
- Area: recipe-worker runtime input hydration
- Observed on: February 22, 2026
- Repro path: `/src/server/llm/test-fixtures/recipes/codex-inbox-git-outbox.yaml`

## Reproduction
1. Use any op input string that includes a sentinel-backed context value plus additional text, e.g.:

```yaml
run: "cat {{ context.environment.inbox }}/input.txt"
```

2. Execute the recipe in runtime context.

3. Observe command execution output showing unresolved sentinel text, for example:

- `cat: __C2_SENTINEL_ARTIFACT_INBOX__/input.txt: No such file or directory`

## Actual Behavior
- Sentinel tokens are replaced only when the full string equals the sentinel.
- Strings that *contain* a sentinel token (e.g. `"__C2_SENTINEL_ARTIFACT_INBOX__/input.txt"`) are not hydrated.
- Ops receive unresolved placeholder paths and fail.

## Expected Behavior
- Sentinel placeholders should never reach op runtime inputs.
- Any sentinel present inside a string value should be replaced with the concrete runtime path.

## Root Cause
In `/src/server/recipe-worker/pkg/ops/op_executor.go`, replacement logic is exact-match only:

- `replaceValue` checks `replacements[val]` and returns unchanged otherwise.
- It does not perform substring replacement for sentinel tokens inside larger strings.

## Impact
- Affects any op whose string inputs embed environment context values with additional text/suffixes.
- Typical failures include inbox/outbox/worktree path usage in command text and prompt text.
- Can silently break recipes depending on how the model/tool interprets leaked paths.

## Suggested Fix
Update sentinel hydration to replace sentinel occurrences within string values, not just whole-string equality.

Example approach:
- Iterate over known sentinel keys in `replacements` and apply `strings.ReplaceAll` to each string value.

## Suggested Regression Tests
1. Add a unit test for `replaceValue`/`replaceSentinelValue` where input is:
   - `"__C2_SENTINEL_ARTIFACT_INBOX__/input.txt"`
   - expected: `"/tmp/.../inbox/input.txt"`
2. Add integration coverage with an op input that uses `{{ context.environment.inbox }}/file` and verifies runtime success.
