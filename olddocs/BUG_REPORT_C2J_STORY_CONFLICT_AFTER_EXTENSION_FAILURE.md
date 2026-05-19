# Bug Report: c2j live extension failure can leave job active with story chapter conflict

## Summary

After the c2ops `codex` extension fails in a live embedded `c2j submit --run --embed`, c2j reports a story upload conflict and leaves the job non-terminal:

```text
Error: job ... did not reach a terminal state after execution: status=ACTIVE: workflow state conflict: chapter ordinal 4 is not appendable; expected 3
```

## Environment

- Date observed: 2026-05-14
- c2j: `github.com/colony-2/c2j v0.0.7-0.20260514020823-bfbbdf5d7254`
- Recipe: `codex-skill-execution-smoke.yaml`
- Command: `C2J_BIN=/tmp/c2j-main-bin/c2j recipe-tests/verify-codex-skill-execution-live.sh`

## Reproduction

Run the live Codex skill execution smoke in an environment without Docker/Podman available:

```bash
C2J_BIN=/tmp/c2j-main-bin/c2j \
  recipe-tests/verify-codex-skill-execution-live.sh
```

The c2ops `codex` extension fails because the runner cannot create a Docker client:

```text
create runner: failed to create docker client: unable to find docker socket; set DOCKER_HOST or ensure Docker/Podman is running
```

Immediately afterward, c2j reports:

```text
story: unexpected status 400 uploading chapter
workflow state conflict: chapter ordinal 4 is not appendable; expected 3
```

## Expected

The extension failure should make the recipe job fail terminally with the original extension error preserved.

## Actual

c2j reports a story chapter ordinal conflict and says the job did not reach a terminal state.

## Impact

Live failure diagnostics are harder to interpret, and callers see a non-terminal job after an extension failure. TS-044 and TS-045 still fail, which is correct, but the final failure should identify the underlying extension error without corrupting story state.

## Notes

The old Go module cache env issue is fixed on c2j main. This is a separate failure path observed after the extension process starts successfully.
