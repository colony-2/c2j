# Bug Report: `recipe test run` mock matching for repeated `node_path` can cause non-terminating loops

## Summary
When a test case declares multiple mock entries with the same `match.node_path` but different outputs (intended for successive invocations), `c2 recipe test run` appears to reuse the first matching mock repeatedly instead of consuming mocks in order. This can cause recipes with retry/resume loops to never reach terminal state and eventually time out.

## Environment
- CLI: `c2` / `colony2`
- Command: `c2 recipe test run`
- Date observed: 2026-03-04
- Working directory: `/src`

## Reproduction
1. Create a recipe test case where the same operation path is expected to run more than once (for example, implementation before and after merge review).
2. In the case `mocks.ops`, add two entries with identical `match.node_path` and different outputs (first call `completed`, second call `incomplete`).
3. Run:
   - `c2 recipe test run --recipe-file /src/new-ticket.yaml --file /src/recipe-tests/new-ticket.scenario.md --case ts-033-merge-without-hash-forces-resolution --parallelism 1 --artifact-mode inline --out-dir /tmp/new-ticket-ts033`

## Actual Result
- Case may loop until server/client timeout.
- Observed error:
  - `context deadline exceeded (Client.Timeout exceeded while awaiting headers)`
- Behavior strongly suggests the first matching mock is reused for every invocation of that `node_path`.

## Expected Result
One of the following deterministic behaviors:
1. Repeated identical `node_path` mocks are consumed in declaration order (first match for first call, second for second call, etc.), or
2. Validation fails early with a clear error when duplicate `match.node_path` entries are ambiguous.

## Impact
- Valid loop/resume recipe behavior becomes difficult to test.
- Tests can appear to “hang” or look like server infinite loops.
- Authors must add artificial node-path indirection to avoid duplicate matches, increasing recipe complexity.

## Suggested Fixes
1. Add invocation-aware matching for `mocks.ops` (consume duplicates in listed order).
2. Add a strict validation mode: reject duplicate `match.node_path` entries unless an explicit `occurrence` selector is provided.
3. Improve run-time diagnostics to report mock hit counts by `node_path` when a case times out.
