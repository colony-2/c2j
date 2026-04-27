#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK_DIR="/tmp/skill-quality-live"
ROOT_CELL_ID="38rXtEAt7DgmefI87FSOKE53fWK"
EXPECTED_REF="gitl.colony2.com/jnadeau/skills/.agents/skills@1362438426e25c733174ea85f248db11af99a8a7"

rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR"

# Keep the local scenario compile-valid even though live verification uses workflow run.
c2 recipe test compile \
  --recipe-file "$PWD/skill-quality-smoke.yaml" \
  --file "$ROOT_DIR/skill-quality-smoke.scenario.md" \
  --out "$WORK_DIR/skill-quality.compiled.json" \
  --strict

if [[ ! -s "$WORK_DIR/skill-quality.compiled.json" ]]; then
  echo "TS-042 failed: skill-quality-smoke scenario did not compile"
  exit 1
fi

c2 recipe update skill-quality-smoke --content-file "$PWD/skill-quality-smoke.yaml" --publish >/tmp/skill-quality-update.log

RUN_JSON="$(c2 --output json workflow run --recipe skill-quality-smoke --cell-id "$ROOT_CELL_ID")"
printf '%s\n' "$RUN_JSON" > "$WORK_DIR/run.json"

JOB_ID="$(printf '%s\n' "$RUN_JSON" | jq -r '.. | objects | .job_id? // empty' | head -n1)"
if [[ -z "$JOB_ID" ]]; then
  JOB_ID="$(printf '%s\n' "$RUN_JSON" | jq -r '.. | objects | .id? // empty' | head -n1)"
fi
if [[ -z "$JOB_ID" ]]; then
  JOB_ID="$(printf '%s\n' "$RUN_JSON" | jq -r '.. | objects | .run_id? // .workflow_id? // empty' | head -n1)"
fi

if [[ -z "$JOB_ID" ]]; then
  echo "TS-042 failed: could not determine workflow job id"
  cat "$WORK_DIR/run.json"
  exit 1
fi

OUTCOME_JSON=""
for _ in $(seq 1 900); do
  if OUTCOME_JSON="$(c2 --output json workflow outcome "$JOB_ID" 2>"$WORK_DIR/outcome.err")"; then
    printf '%s\n' "$OUTCOME_JSON" > "$WORK_DIR/outcome.json"
    break
  fi
  if grep -q "outcome not available yet" "$WORK_DIR/outcome.err"; then
    sleep 2
    continue
  fi
  echo "TS-042 failed: workflow outcome request errored"
  cat "$WORK_DIR/outcome.err"
  exit 1
done

if [[ -z "$OUTCOME_JSON" ]]; then
  echo "TS-042 failed: workflow outcome not available before timeout"
  exit 1
fi

status_path='
  .status //
  .response.status //
  .response.outcome.status //
  .response.workflow.status //
  ""
'

OUTPUTS_JSON="$(jq -c '
  .outputs //
  .response.outputs //
  .response.output.outputs //
  .response.outcome.outputs //
  .response.workflow.outputs //
  {}
' "$WORK_DIR/outcome.json")"

STATUS="$(jq -r "$status_path" "$WORK_DIR/outcome.json")"
if [[ "$STATUS" != "completed" && "$STATUS" != "" ]]; then
  echo "TS-042 failed: workflow did not complete successfully (status=$STATUS)"
  cat "$WORK_DIR/outcome.json"
  exit 1
fi

assert_bool() {
  local key="$1"
  local expected="$2"
  local actual
  actual="$(printf '%s\n' "$OUTPUTS_JSON" | jq -r --arg key "$key" '.[$key]')"
  if [[ "$actual" != "$expected" ]]; then
    echo "TS-042 failed: expected $key=$expected, got $actual"
    printf '%s\n' "$OUTPUTS_JSON"
    exit 1
  fi
}

assert_str() {
  local key="$1"
  local expected="$2"
  local actual
  actual="$(printf '%s\n' "$OUTPUTS_JSON" | jq -r --arg key "$key" '.[$key]')"
  if [[ "$actual" != "$expected" ]]; then
    echo "TS-042 failed: expected $key=$expected, got $actual"
    printf '%s\n' "$OUTPUTS_JSON"
    exit 1
  fi
}

assert_bool "triage_local_ok" "true"
assert_bool "triage_local_rationale_nonempty" "true"
assert_bool "triage_frontend_redirect" "true"
assert_str "triage_frontend_target" "frontend"
assert_bool "requirements_review_good_ok" "true"
assert_bool "requirements_count_nonzero" "true"
assert_bool "requirements_cross_cell_false" "true"
assert_bool "requirements_targets_valid" "true"
assert_bool "requirements_acceptance_nonempty" "true"
assert_bool "requirements_open_questions_reasonable" "true"
assert_bool "requirements_bad_review_rejects" "true"
assert_bool "requirements_bad_blockers_nonempty" "true"
assert_bool "implementation_review_good_ok" "true"
assert_bool "implementation_requires_dependency_jobs_false" "true"
assert_bool "implementation_local_steps_nonzero" "true"
assert_bool "implementation_has_validation_step" "true"
assert_bool "implementation_bad_review_rejects" "true"
assert_bool "implementation_bad_blockers_nonempty" "true"
assert_bool "outcome_review_good_ok" "true"
assert_bool "outcome_validation_commands_nonempty" "true"
assert_str "outcome_repo_glob" ".c2/tests/*.md"
assert_bool "outcome_statement_files_exist" "true"
assert_bool "outcome_statement_annotations_present" "true"
assert_bool "outcome_positive_and_negative_present" "true"
assert_bool "outcome_bad_review_rejects" "true"
assert_bool "outcome_bad_blockers_nonempty" "true"

RESOLVED_REF="$(printf '%s\n' "$OUTPUTS_JSON" | jq -r '.triage_skill_ref_resolved // ""')"
if [[ "$RESOLVED_REF" != "$EXPECTED_REF" ]]; then
  echo "TS-043 failed: expected resolved ref $EXPECTED_REF, got $RESOLVED_REF"
  printf '%s\n' "$OUTPUTS_JSON"
  exit 1
fi

echo "TS-042 and TS-043 passed"
