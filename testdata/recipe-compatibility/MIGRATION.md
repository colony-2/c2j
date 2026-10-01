# Native test coverage audit

Baseline: recipes `3d3f24e`, before removal of the external harnesses. This is a
requirements mapping, not a comparison of test counts. Generic runtime tests
must not stand in for assertions about a production recipe's prompts or routing.

## Recipe requirements

Paths in this table are relative to the recipes repository.

| Former assertions | Replacement and checks |
| --- | --- |
| `verify-default-recipes.py`: 26 routing cases for each entrypoint | `recipe-tests/{build,evolve}.scenario.md`: phase order, target scope, invalid outcomes, direct feedback, repeated feedback summaries, redesign/reapproval, exact resumed checkpoint, merge gating. |
| Real schema gates, including malformed/missing result fields and sessions | `agent-gates.test.yaml` (26 cases), `consultation-gates.test.yaml` (8 cases); normal rule_gate runs. |
| Maintained test plan publication; verification pass/fail/timeout/missing hook and exact exported artifact names | `test-plan.test.yaml`, `verification.test.yaml`; actual commands and file publication. |
| `verify-dependencies.py`: decisions for empty/duplicate/prior dependencies, failures, cancellation and unmerged results | `dependencies.test.yaml` (10 cases). |
| Submitted children, scoped agent, repeated rounds, failed children/recovery, unmerged results, missing session, invalid result | `dependency-runtime.test.yaml`: actual broker submission/await, exact checkpoint routing, all dependency IDs and artifact paths in resumed input, mixed and all-failed recovery, failure messages. |
| Retained history after later feedback; no new children | `dependency-feedback.test.yaml`: a second invocation of the production agent receives the earlier checkpoint, identical dependency history and evidence; only two child evidence calls occur. Cross-job transport is separately tested in c2j. |
| `verify-implementation-consultations.py`: advice, bug/evolve redesign, repeated B/C/B consultations, missing mandate, outside ownership, failed history, invalid/missing foreign result/checkpoint, illegal children, unavailable cell | `implementation-consultations.test.yaml`: exact cell order and checkpoints, prompt routing, preserved candidate, foreign experiment absence, returned history and unchanged upstream repositories. |
| Design dialogue, reuse in implementation, later feedback | `consultation-{design,reuse,feedback}.test.yaml`: actual production includes, exact checkpoint continuation and cell order. |
| `verify-development-lifecycle.py`: full build/evolve consultation → redesign → approval → child → recovery → merge | `{build,evolve}-lifecycle.test.yaml`: actual child workflows and merges, review order, verification failure/recovery, evidence provenance, one squash per cell, exact final changed files. |
| `verify-native-reviews.py`: two/four documents, text/file revisions, redesign, approval with edits | `{build,evolve}-reviews.test.yaml`: eight actual reviews, current documents/receipts, annotation routing and clearing, implementation checkpoint continuity, fresh design sessions, one final merge. |
| Existing examples/jobs/new-ticket/Superpowers cases; real rule_gate schema checks | Self-contained scenario files remain discovered and executed. Invalid-rule-input case requires its specific error. |
| Superpowers inline primary missing-reproduction behavior | `recipes/superpowers/tests/superpowers-inline.test.yaml`. |

The audit found missing assertion strength after the first conversion: dependency
history/evidence delivery, all-failed recovery, consultation workspace contents
and exact checkpoint routing, and absence of stale review annotations. These
checks are now explicit. A passing count of 220 alone did not prove equivalence.

## Runtime requirements

Paths and test names below are in c2j and run independently of recipes.

| Former external harness guarantee | Concrete c2j coverage |
| --- | --- |
| Parent blocks for pending child; worker replacement; cancellation unblocks; no duplicate submission | `TestChildWaitSurvivesWorkerReplacementAndCancellation` in `child_wait_restart_test.go`: durable SQLite close/reopen, real pending status, cancellation, exact child listing and probe counts. |
| Already-finished child can be consumed after restart | `TestAwaitAlreadyFinishedChildAfterWorkerReplacement` in the same file. |
| A fresh consultation sees upstream movement, discards prior experiments, preserves parent candidate | `TestFreshConsultationSeesAdvancedUpstreamWithoutLosingCandidate` in `workspace_fresh_upstream_test.go`; explicit scopes and nested snapshots also in `workspace_integration_test.go`. |
| Child merge does not collide with parent snapshot or change child evidence keys | `TestAwaitMergedChildPreservesParentSnapshotAndChildEvidence`; replay compatibility in `TestReplayLegacyArtifactOrderWithRealJobDB`. |
| Compiled child includes and broker lineage | `pkg/childbroker/compiled_recipe_test.go` and broker tests. |
| Required/optional child failures and review-pack artifacts | `TestRecipeChildFixtures` in `pkg/child/test-fixtures`, `pkg/ops/recipe/child_group_test.go`, `pkg/worker/compiler/child_group_test.go`. |
| Child attachment forwarding | `TestRun_ForwardsSubmittedArtifactToChildRecipe` in CLI submit integration tests and child fixtures. |
| Build/evolve target-cell defaults, fallback, local wrappers, remote includes | `TestRun_ConventionsUseTargetCellNotSubmittingCell`, `root_source_conventions_test.go`, `root_source_test.go`, `inline_resolution_test.go`. |
| Invalid/stale review answers, scoped attachments, original document refs, replay across database reopen | `TestReviewSQLiteAttachmentsReplayAndStaleSubmission`, `TestReviewRecipeWithAttachmentsAndWorkspace`. |
| Object checkpoint branches, failed attempt isolation, cross-job continuation, hidden state, extension envelope | `TestObjectCheckpointRecipeBranchesAndReplay`, `TestObjectCheckpointsAcrossParallelChildJobsAndLaterJob`, `TestExtensionObjectCheckpointThroughIncludedStateRecipe`; invalid refs in `pkg/objects/store_test.go`. |
| CEL histories survive JSON/jq serialization | `pkg/template/native_result_test.go`. |
| Real merge and non-fast-forward rejection with unchanged upstream | `pkg/git/squashrebasemerge/operation_test.go`, especially `TestRunSquashRebaseMerge_FastForwardFailsWhenRemoteAdvanced`. |
| CLI compile/validate/run, discovery, empty/invalid selection, failure reports | `cmd/c2j/internal/testjob/{runner,discovery,runtime}_test.go`. |

Worker replacement and moving-upstream tests above were added during this audit;
the earlier migration map cited broader tests without covering those exact
transitions. Native report observations now have their own persistence test.

## Adapter and retired coverage

Codex's private session home, rollout/SQLite import/export, WAL state, legacy
input rejection and corrupt checkpoint detection belong to c2ops. At pinned
revision `ded76dfbd877d3d0749e509844ecdbc57197b572`, runnable replacements include
`TestSessionBranchesRestoreExactCheckpoint`,
`TestSessionExportIncludesWALAndRejectsIncompleteState`,
`TestSessionRejectsLegacyInputsAndInvalidState`, `TestRunFailureDoesNotPublish`,
`TestSessionRejectsUnsupportedMetadataAndCorruptDatabase`, skill continuation,
and extension command tests. Generic c2j object transport is exercised separately
through an actual extension process. The former combined Codex emulator is not
retained as an additional end-to-end test.

`verify-review-contract.py` checked the abandoned hash/download-URL proposal.
Its TS-152–156 assertions are intentionally retired, not counted as preserved.
The implemented questions/documents review behavior is covered above.

## Verification and limits

The native compatibility job is implemented directly in `.github/workflows/test.yaml`.
There is no CI migration patch or external fixture server. Local verification
uses a clean c2j source tree to exclude unrelated uncommitted work, detached
companion checkouts, the workflow's URL rewrite, and an empty selector cache.

Live model/skill suites remain explicitly opt-in; structural validation is not
execution coverage. Local Linux ARM64 results do not claim that hosted GitHub CI,
Linux AMD64, macOS, Docker/Shai, or live model work ran. Environment-dependent Go
tests may skip when their dependency is unavailable. The three live suites are
reported as excluded, never as passed.

Audit verification (2026-10-01): clean Go 1.26.1 `go test -tags=integration ./...`
passed. Runner/fixture race tests passed, and the three new compiler regressions
passed three repetitions under the race detector. The fresh-checkout native
command passed **222 cases in 51 suites**, with three live suites excluded.
The later strengthened build/evolve lifecycle assertions passed targeted runs.
Pinned c2ops `go test ./pkg/codex ./internal/extensioncmd` also passed.

All 12 consultation cases also passed with an exact dependency-context assertion.
A mutation that removed history from the delivered prompt passed the earlier
checks and failed that new assertion. Final directory validation passed including
live declarations; it does not substitute for live execution.

## CI broker interface regression

The initial native child fixture rewrote the broker's advertised host to
`127.0.0.1`, preserving its dynamic port. That worked with a wildcard listener
inside the local container but failed on Linux CI when the broker listened only
on the Docker bridge interface. The resulting submission failure sent the
lifecycle into a recovery review with no scripted response.

Host-side fixtures now use an invocation-scoped local broker client. It derives
the address from the actual listener, uses the same authenticated submit handler,
and avoids ambient proxies. Container-facing endpoints remain unchanged. Broker
listeners explicitly use IPv4, matching the interface discovery they already use.
`TestLocalSubmitUsesBoundInterfaceAndDynamicPort` covers loopback, another bound
address, wildcard binding, and broker closure; it reproduces the refused
connection produced by the old rewrite. The native submit/await integration
regression verifies the worker supplies the active broker context.

Verification: a disposable build forced container-facing listeners onto
`127.0.0.2`. The original runner reproduced both connection refusals and exactly
`no input response fixture for build/develop/root/approve_plan/input`; the fixed
runner passed the same lifecycle with that binding. The normal compatibility
run passed all 222 cases in 51 suites. The full integration-tagged Go suite,
focused race tests, and ten repeated binding-regression runs also passed.
