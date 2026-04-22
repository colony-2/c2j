# Recipe Test Statements

These statements are intended to drive `c2 recipe test` cases.

## Statement Catalog

| ID | Test statement | Relevant file(s) | Importance | Type / dependencies | Polarity |
|---|---|---|---|---|---|
| TS-001 | Triage marks appropriate cell tickets as `cell_is_appropriate=true`. | `new-ticket-triage.yaml` | High | Integration (`recipe_case`); deps: `codex.exec` mock | Positive |
| TS-002 | Triage marks out-of-cell tickets as `cell_is_appropriate=false`. | `new-ticket-triage.yaml` | High | Integration (`recipe_case`); deps: `codex.exec` mock | Positive |
| TS-003 | Invalid recommended cell yields `recommended_cell_is_valid=false`. | `new-ticket-triage.yaml` | High | Integration (`recipe_case`); deps: `cells()` context | Negative |
| TS-004 | Triage emits a triage decision artifact payload for downstream routing. | `new-ticket-triage.yaml` | Medium | Integration (`recipe_case`); deps: artifact capture | Positive |
| TS-005 | Requirements planning emits `requirements/plan.json`, `requirements/index.md`, and requirement markdown artifacts. | `new-ticket-requirements-planning.yaml` | High | Integration (`recipe_case`); deps: `codex.exec` mocks | Positive |
| TS-006 | Requirements outputs expose dependency order and cross-cell flags from planning payload. | `new-ticket-requirements-planning.yaml` | High | Integration (`recipe_case`); deps: JSON output parsing | Positive |
| TS-007 | Requirements planning accepts user feedback input and returns updated summary outputs. | `new-ticket-requirements-planning.yaml` | High | Integration (`recipe_case`); deps: input plumbing | Positive |
| TS-008 | Blocking API review sets `api_review_ok=false` and returns blocking issues list. | `new-ticket-requirements-planning.yaml` | High | Integration (`recipe_case`); deps: contrarian review output | Negative |
| TS-009 | Contrarian review emits `requirements/api-review.json` artifact. | `new-ticket-requirements-planning.yaml` | High | Integration (`recipe_case`); deps: artifact capture | Positive |
| TS-010 | Planning and review artifacts coexist in one requirements-planning execution. | `new-ticket-requirements-planning.yaml` | High | Integration (`recipe_case`); deps: multi-artifact output | Positive |
| TS-011 | Implementation uses continue-session path when `session_id` is provided. | `ticket-implement.yaml` | High | Integration (`recipe_case`); deps: state initial branching | Positive |
| TS-012 | Implementation uses new-session path when `session_id` is absent. | `ticket-implement.yaml` | High | Integration (`recipe_case`); deps: state initial branching | Positive |
| TS-013 | Implementation returns non-empty `assistant_summary` output for reviewer context and next-session reuse. | `ticket-implement.yaml` | Medium | Integration (`recipe_case`); deps: `codex.exec` mock outputs | Positive |
| TS-014 | Validation provided-command path persists full and tail validation artifacts. | `ticket-validate.yaml` | High | Integration (`recipe_case`); deps: `command_execution` mock artifacts | Positive |
| TS-015 | Validation suggested-command path executes and reports pass status. | `ticket-validate.yaml` | High | Integration (`recipe_case`); deps: `input` + `command_execution` mocks | Positive |
| TS-016 | Validation custom-command path executes and persists output tail artifact. | `ticket-validate.yaml` | High | Integration (`recipe_case`); deps: `input` + `command_execution` mocks | Positive |
| TS-017 | Validation failure sets `passed=false` and non-zero `exit_code`. | `ticket-validate.yaml` | High | Integration (`recipe_case`); deps: failure output mapping | Negative |
| TS-018 | Validation outputs stable artifact contract keys for full and tail logs. | `ticket-validate.yaml` | Medium | Integration (`recipe_case`); deps: output contract | Positive |
| TS-019 | Merge prompts for missing upstream details before approval. | `ticket-merge.yaml` | High | Integration (`recipe_case`); deps: `input` prompt path | Positive |
| TS-020 | Merge approval `cancel` exits without merge hash output. | `ticket-merge.yaml` | High | Integration (`recipe_case`); deps: approval branching | Negative |
| TS-021 | Merge approval `merge` returns non-empty `merged_hash`. | `ticket-merge.yaml` | High | Integration (`recipe_case`); deps: `squashrebasemerge` mock | Positive |
| TS-022 | Prompted merge path executes merge and returns prompted target branch. | `ticket-merge.yaml` | High | Integration (`recipe_case`); deps: prompt + merge path | Positive |
| TS-023 | Merge outputs expose `target_branch` from merge operation result. | `ticket-merge.yaml` | Medium | Integration (`recipe_case`); deps: output mapping | Positive |
| TS-024 | `recipe test compile` generates canonical IR JSON artifact. | `guides/RECIPE_TESTING_CLI_USER_GUIDE.md` | Medium | CLI integration; deps: `c2 recipe test compile` | Positive |
| TS-025 | `recipe test validate` returns non-zero when case filters select no cases. | `guides/RECIPE_TESTING_CLI_USER_GUIDE.md` | Medium | CLI integration; deps: `c2 recipe test validate` | Negative |
| TS-026 | `recipe test run` writes `summary.json`, `summary.md`, and per-case results. | `guides/RECIPE_TESTING_CLI_USER_GUIDE.md` | Medium | CLI integration; deps: `c2 recipe test run` | Positive |
| TS-027 | Scenario markdown without fenced YAML/JSON fails compile. | `guides/RECIPE_TESTING_CLI_USER_GUIDE.md` | Medium | CLI integration; deps: `c2 recipe test compile` | Negative |
| TS-028 | Implementation planning emits `implementation/plan.json`, `implementation/index.md`, and per-requirement markdown artifacts. | `new-ticket-implementation-planning.yaml` | High | Integration (`recipe_case`); deps: `codex.exec` mocks | Positive |
| TS-029 | Implementation planning outputs expose dependency gating fields and dependency order. | `new-ticket-implementation-planning.yaml` | High | Integration (`recipe_case`); deps: JSON output mapping | Positive |
| TS-030 | Blocking compatibility review sets `compat_review_ok=false` and returns blocking issues. | `new-ticket-implementation-planning.yaml` | High | Integration (`recipe_case`); deps: contrarian review output | Negative |
| TS-031 | Approved implementation plan requiring dependency tickets creates child tickets and enters waiting state. | `new-ticket.yaml` | High | Integration (`recipe_case`); deps: `ticket.manage` + child recipe mocks | Positive |
| TS-032 | Merge decision `cancel_ticket` updates workflow to cancelled path without merge. | `new-ticket.yaml` | High | Integration (`recipe_case`); deps: input branching + `ticket.manage` mock | Negative |
| TS-033 | Merge request without local hash returns to implementation instead of executing merge. | `new-ticket.yaml` | High | Integration (`recipe_case`); deps: ready-to-merge branching | Negative |
| TS-034 | Successful merge path marks ticket completion (`ticket_done=true`) with non-empty merged hash. | `new-ticket.yaml` | High | Integration (`recipe_case`); deps: `local_hash` input + `squashrebasemerge` mock | Positive |
| TS-035 | Implementation-reported cross-cell bugs create child bug tickets before continuing workflow. | `new-ticket.yaml` | High | Integration (`recipe_case`); deps: `codex.exec` pendingDependencies + `ticket.manage` mock | Positive |
| TS-036 | Implementation user questions pause flow for structured user input, then continue the same Codex session. | `new-ticket.yaml` | High | Integration (`recipe_case`); deps: `codex.exec` incompleteCategory + `input` + repeated `codex.exec` mock | Positive |
| TS-037 | Outcome determination emits outcome artifacts and identifies authoritative test statements at `.c2/tests/*.md`. | `new-ticket-outcome-determination.yaml` | High | Integration (`recipe_case`); deps: `codex.exec` mocks + artifact capture | Positive |
| TS-038 | Outcome outputs expose update requirement flag and validation command plan. | `new-ticket-outcome-determination.yaml` | High | Integration (`recipe_case`); deps: JSON output mapping | Positive |
| TS-039 | Blocking outcome review sets `review_ok=false` and returns blocking issues. | `new-ticket-outcome-determination.yaml` | High | Integration (`recipe_case`); deps: contrarian review output | Negative |
| TS-040 | Main ticket runs outcome review before implementation and uses outcome validation commands by default. | `new-ticket.yaml` | High | Integration (`recipe_case`); deps: outcome child recipe + validation input mapping | Positive |
| TS-041 | Implementation requesting statement changes routes through pre-implementation review instead of direct `.c2/tests/*.md` edits. | `new-ticket.yaml` | High | Integration (`recipe_case`); deps: `codex.exec` incompleteCategory + input gate | Positive |
| TS-042 | Live skill bundle produces the intended behavior: correct cell triage, local-only compatible requirements, safe implementation planning, canonical outcome statements, and contrarian rejection of deliberately bad artifacts. | `skill-quality-smoke.yaml`, `recipe-tests/verify-skill-quality-live.sh` | High | Live workflow integration; deps: published `skill-quality-smoke`, live `codex.exec`, pinned git skill ref, `jq` | Positive |
| TS-043 | Live skill execution reports the pinned HTTPS repo ref resolved to the expected concrete commit hash. | `skill-quality-smoke.yaml`, `recipe-tests/verify-skill-quality-live.sh` | High | Live workflow integration; deps: live `codex.exec`, `jq`, HTTPS-accessible skill repo | Positive |

## Notes for Test Authoring

- Prefer `recipe_case` with explicit op mocks for deterministic branch coverage under test-policy sandboxing.
- Op mocks are single-use per invocation; if a node/op can run multiple times, add one mock entry per expected invocation.
- Use `integration_case` only when external workflow context is required and available in the harness.
- Keep artifact assertions focused on outbox contract files, not assistant summary text.
- For live skill checks, use workflow-run acceptance tests that validate authoring quality and contrarian rejection behavior, not just recipe node wiring.
