# Sequence node `when: false` reproduction

The recipe contains one `sleep` task with `when: false`. Expected behavior is a
successful recipe with `finished: true` and zero calls to the task. The output
does not reference the skipped task, so missing task outputs cannot cause the
failure.

Run from the repository root against the current source:

```sh
go run ./cmd/c2j test run \
  --file testdata/repros/sequence-when-false/recipe.test.yaml \
  --out-dir /tmp/c2j-sequence-when-false \
  --parallelism 1 --case-timeout 20s

go test ./pkg/recipetest -run '^TestSequenceNodeWhen' -count=1 -v
go test ./pkg/worker/compiler -run '^TestNodeWhenValidationModes$' -count=1 -v
```

Observed before the fix on 2026-11-13:

| Case | Expected calls | Actual calls | Result |
| --- | ---: | ---: | --- |
| Native runtime, `when: false` | 0 | 1 | FAIL |
| Fast executor, `when: false` | 0 | 1 | FAIL |
| Go regression, `when: false` | 0 | 1 | FAIL |
| Go regression, `when: 'false'` | 0 | 1 | FAIL |
| Go regression, `when: '1 == 2'` | 0 | 1 | FAIL |
| Go control, `when: true` | 1 | 1 | PASS |
| Go control, omitted `when` | 1 | 1 | PASS |

The native case uses disposable local JobDB/Git fixtures and no mocks. Before
the fix, its call report recorded `sleep` returning `completed: true` and an
actual duration of about 1 ms. Both suites failed their zero-call assertion;
the YAML suite also
failed `node_not_executed`.

## Cause

`pkg/recipe/node.go` declares `NodeMetadata.When` as a CEL expression for
conditional execution. The Go regression verifies that parsing preserves each
expression and that CEL evaluates all three false conditions to false.

`DefaultRecipeExecutor.innerSequence` in `pkg/worker/compiler/compiler.go`
calls `ExecuteNode` for every sequence child. Previously, `ExecuteNode`
dispatched to `ExecuteOp` without evaluating `metadata.When`, and the operation
scope also contained no node-condition check. The node-level condition was
therefore ignored during execution. Transition, catch, and child-group-entry
conditions have separate evaluation paths and do not supply this missing check.

## Fix and regression coverage

`ExecuteNode` now evaluates the condition against the enclosing resolution
context before dispatch or node preparation. False nodes return without task
calls or outputs; invalid or non-boolean conditions fail with the node ID.
Validation of all paths still checks false-node bodies; path-only validation
skips them.

Both YAML cases now pass with zero calls. The Go regressions exercise the real
runtime and fast executor for literal conditions, input/variable/prior-output
conditions, continuation after skipping, skipped nested sequences, skipping
input/variable/workspace preparation, and invalid conditions. Separate compiler
tests cover both validation modes. The real runtime cases use unmocked sleeps
in disposable local fixtures.
