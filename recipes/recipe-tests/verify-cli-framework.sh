#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK_DIR="/tmp/recipe-cli-checks"
rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR"

# TS-024: compile writes canonical IR JSON
c2 recipe test compile \
  --recipe-file "$PWD/new-ticket-triage.yaml" \
  --file "$ROOT_DIR/new-ticket-triage.scenario.md" \
  --out "$WORK_DIR/triage.compiled.json" \
  --strict

if [[ ! -s "$WORK_DIR/triage.compiled.json" ]]; then
  echo "TS-024 failed: compiled IR not written"
  exit 1
fi

# TS-025: validate returns non-zero when case selection is empty
set +e
c2 recipe test validate \
  --recipe-file "$PWD/new-ticket-triage.yaml" \
  --file "$ROOT_DIR/new-ticket-triage.scenario.md" \
  --case does-not-exist \
  --parallelism 1 >/tmp/ts025.log 2>&1
RC_VALIDATE=$?
set -e
if [[ "$RC_VALIDATE" -eq 0 ]]; then
  echo "TS-025 failed: validate returned zero for empty case selection"
  cat /tmp/ts025.log
  exit 1
fi

# TS-026: run writes summary and per-case result artifacts
c2 recipe test run \
  --recipe-file "$PWD/new-ticket-triage.yaml" \
  --file "$ROOT_DIR/new-ticket-triage.scenario.md" \
  --case ts-001-appropriate-cell \
  --parallelism 1 \
  --artifact-mode inline \
  --out-dir "$WORK_DIR/run" \
  --evaluation-mode enforce

for path in \
  "$WORK_DIR/run/summary.json" \
  "$WORK_DIR/run/summary.md" \
  "$WORK_DIR/run/cases/ts-001-appropriate-cell/result.json"
do
  if [[ ! -s "$path" ]]; then
    echo "TS-026 failed: missing expected run artifact $path"
    exit 1
  fi
done

# TS-027: scenario markdown without fenced block fails compile
set +e
c2 recipe test compile \
  --recipe-file "$PWD/new-ticket-triage.yaml" \
  --file "$ROOT_DIR/invalid-no-fence.md" \
  --out "$WORK_DIR/invalid.compiled.json" \
  --strict >/tmp/ts027.log 2>&1
RC_COMPILE=$?
set -e
if [[ "$RC_COMPILE" -eq 0 ]]; then
  echo "TS-027 failed: compile unexpectedly succeeded for bad scenario markdown"
  cat /tmp/ts027.log
  exit 1
fi

echo "TS-024..TS-027 passed"
