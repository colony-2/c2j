# Bug Report: c2ops Codex op hangs during live skill smoke

Detected: 2026-05-16 UTC, during the live Codex skill execution smoke.

## Summary

The live `c2ops` Codex op started successfully but did not complete a minimal file-writing task. It ran for more than 20 minutes without creating the required marker file or outbox status artifacts.

This ticket is about the Codex op hanging or failing to make progress. The separate c2j timeout cleanup issue observed in the same run is tracked in `guides/BUG_REPORT_C2J_EXTENSION_OP_TIMEOUT_DOES_NOT_KILL_PROCESS_TREE.md`.

## Environment

- Repo: `/src`
- c2j checkout: `/c2j` on `main`, HEAD `fc19fd1 Fix passthrough recipe test op paths`
- c2j binary used: `/tmp/c2j-local-bin/c2j`
- c2ops checkout: `/c2ops` on `main`, HEAD `775ab63 fix codex op path defaults`
- Recipe op selector: `git+https://github.com/colony-2/c2ops.git//codex@main`
- Codex CLI invocation included `--dangerously-bypass-approvals-and-sandbox`

## Reproduction

```bash
/tmp/c2j-local-bin/c2j test run \
  --recipe-file /src/codex-skill-execution-smoke.yaml \
  --file /src/recipe-tests/codex-skill-execution-smoke.scenario.md \
  --parallelism 1 \
  --artifact-mode inline \
  --out-dir /tmp/run-codex-skill-execution-smoke \
  --evaluation-mode enforce \
  --case-timeout 20m
```

Observed case:

- `ts-044-live-codex-skill-executes-and-writes-marker`

## Expected

The c2ops Codex op should either:

1. Complete the simple requested repo edit and write the required outbox status artifacts, or
2. Fail quickly with a clear error explaining why Codex could not execute the task.

For this smoke, the op was expected to create:

- `.c2/live-codex-skill-execution/result.json`
- `outbox/implementation/latest-status.json`
- `outbox/implementation/progress.ndjson`
- `outbox/implementation/summary.md`

## Actual

The c2ops Codex wrapper launched Codex successfully. The process tree included:

- the selector-backed c2ops Codex wrapper binary
- `node /usr/local/bin/codex exec --experimental-json --dangerously-bypass-approvals-and-sandbox ...`
- the native Codex executable

After more than 20 minutes:

- `.c2/live-codex-skill-execution/result.json` had not been created.
- `outbox/implementation/latest-status.json` had not been created.
- `outbox/implementation/progress.ndjson` had not been created.
- `outbox/implementation/summary.md` had not been created.
- The worktree only contained setup artifacts and `.c2/tests/codex-skill-execution-smoke.md`.

## Notes

The c2ops path/default issue was not reproduced. The live op successfully started through `codex@main` and used the new visible path layout:

- `workdir/inbox`
- `workdir/outbox`
- `workdir/worktree/<cell>`

The remaining failure is that the Codex invocation did not produce the expected task output or fail with an actionable error.

## Reevaluation 2026-05-18 UTC

Status: still open with `/c2ops` on `main` at `98980bb fix codex live skills and session handling`.

Changes observed:

- `/c2j` on `main` at `2863bbb Overlay recipe test timeouts onto execution` now enforces `--case-timeout` and cleans up the Codex subprocess tree.
- The live smoke scenario was updated to provide enough passthrough mocks for all three `command_execution` nodes.
- `codex-skill-execution-smoke.yaml` was updated so boolean values passed through `command_execution.inputs.env` are rendered as strings.

Reevaluation command:

```bash
/tmp/c2j-local-bin/c2j test run \
  --recipe-file /src/codex-skill-execution-smoke.yaml \
  --file /src/recipe-tests/codex-skill-execution-smoke.scenario.md \
  --parallelism 1 \
  --artifact-mode inline \
  --out-dir /tmp/run-codex-skill-execution-smoke-reeval-fixed \
  --evaluation-mode enforce \
  --case-timeout 20m
```

Results:

- `ts-044-live-codex-skill-executes-and-writes-marker` did not hang; Codex returned in about 83 seconds and wrote the requested outbox artifacts.
- That case still failed because Codex wrote `.c2/live-codex-skill-execution/result.json` under the worktree root instead of under the current cell path. This is tracked separately in `guides/BUG_REPORT_C2OPS_CODEX_OP_USES_WORKTREE_ROOT_INSTEAD_OF_CELL_PATH.md`.
- `ts-045-live-codex-skill-contract-artifacts-and-ref` reproduced the hang/progress failure. Codex started, logged local read commands, then produced no required marker or outbox status artifacts before c2j killed it at the 20-minute case timeout.
- No Codex/c2j child processes remained after the timeout, which confirms the c2j cleanup issue is fixed but the c2ops Codex progress issue is not.

Additional note:

- c2ops documents an `idle_timeout` default of `5m`, but this reevaluation did not observe the Codex op returning an idle-timeout error before the outer 20-minute c2j case timeout.
