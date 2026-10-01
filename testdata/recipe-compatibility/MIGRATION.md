# Native test migration inventory

This inventory separates assertions in the recipes repository from execution
guarantees. Existing coverage stays until the replacement is runnable. The
recipes source examined for this migration is `3d3f24e`.

| Source family | Runtime guarantee covered in c2j | Recipe assertions to retain |
| --- | --- | --- |
| CLI framework and inline/include scripts | `cmd/c2j/internal/testjob/runner_test.go`; broker compiled-include regression in `pkg/childbroker/compiled_recipe_test.go` | Each published recipe validates and its declared cases pass. |
| Dependencies | `pkg/ops/recipe/child_job_id_test.go`, `await_result_soft_test.go`, `child_group_test.go`; native runner submits/awaits through the broker in `cmd/c2j/internal/testjob/runtime_test.go` | Successful/failed/cancelled/unmerged dependency decisions, multiple rounds and history retention. |
| Consultation workspaces | `pkg/worker/compiler/workspace_integration_test.go`, `workspace_replay_test.go`, `child_snapshot_integration_test.go` | Consultation selection, scoped prompts, session routing, agreed handoffs and retained feedback. |
| Full development lifecycle | `TestAwaitMergedChildPreservesParentSnapshotAndChildEvidence`, `TestReplayLegacyArtifactOrderWithRealJobDB` | Approval before work/merge, verification evidence, dependency results and final exports. |
| Native reviews | `pkg/input/review_test.go`, `pkg/input/test-fixtures/review_test.go`; runtime suite exercises two documents and an uploaded annotation | Design/test-plan and implementation/verification document selection, revisions and acceptance. |
| Generic session objects | `pkg/objects/store_test.go`, `pkg/worker/compiler/objects_integration_test.go`, `objects_input_test.go` | A recipe forwards the intended session checkpoint to each phase. |
| Codex adapter/session import-export | c2ops-owned behavior; retain existing coverage until a c2ops handoff has a runnable replacement | Do not mistake an adapter emulator for recipe correctness. |
| Template history/JSON | `pkg/template/native_result_test.go`, including `TestJSONHelpersNormalizeCELHistoryValues` | Correct histories and evidence appear in prompts and outputs. |

The new native runner itself has regressions for Git state persistence, object
publication, broker child submission, document reviews and attachment responses,
unused fixtures, missing fixtures, execution failure, timeout and parallel case
isolation. CEL assertion tests reject false/invalid expressions. Directory tests
cover nested discovery, relative paths, invalid declarations, live selection,
empty selection and retained failure reports.

Stage 2 must map individual recipe case IDs and test statements before removing
their Python assertions. Presence of an equivalent runtime test does not replace
a recipe-specific decision, prompt or document-routing assertion.
