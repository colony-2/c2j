#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK_DIR="/tmp/recipe-test-suite-all"
rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR"

run_suite() {
  local recipe_file="$1"
  local suite_file="$2"
  local name="$3"

  echo "== $name: compile =="
  c2 recipe test compile \
    --recipe-file "$PWD/$recipe_file" \
    --file "$ROOT_DIR/$suite_file" \
    --out "$WORK_DIR/${name}.compiled.json" \
    --strict

  echo "== $name: validate =="
  c2 recipe test validate \
    --recipe-file "$PWD/$recipe_file" \
    --file "$ROOT_DIR/$suite_file" \
    --parallelism 1

  echo "== $name: run =="
  c2 recipe test run \
    --recipe-file "$PWD/$recipe_file" \
    --file "$ROOT_DIR/$suite_file" \
    --parallelism 1 \
    --artifact-mode inline \
    --out-dir "$WORK_DIR/${name}.run" \
    --evaluation-mode enforce
}

run_suite "new-ticket-triage.yaml" "new-ticket-triage.scenario.md" "triage"
run_suite "new-ticket-requirements-planning.yaml" "new-ticket-requirements-planning.scenario.md" "requirements"
run_suite "new-ticket-implementation-planning.yaml" "new-ticket-implementation-planning.scenario.md" "implementation_planning"
run_suite "new-ticket-outcome-determination.yaml" "new-ticket-outcome-determination.scenario.md" "outcome_determination"
run_suite "new-ticket.yaml" "new-ticket.scenario.md" "new_ticket"
run_suite "job-implement.yaml" "job-implement.scenario.md" "implement"
run_suite "job-validate.yaml" "job-validate.scenario.md" "validate"
run_suite "job-merge.yaml" "job-merge.scenario.md" "merge"

echo "TS-001..TS-041 (recipe suites) passed."
"$ROOT_DIR/verify-cli-framework.sh"
"$ROOT_DIR/verify-skill-quality-live.sh"

echo "TS-001..TS-043 passed."
