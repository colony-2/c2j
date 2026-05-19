# Bug Report: c2ops Codex still requires Docker when c2j extension sandbox is disabled

## Summary

Live recipes can set the reserved c2j extension sandbox input:

```yaml
inputs:
  sandbox:
    type: none
```

That should run the selector-backed extension process without the c2j wrapper sandbox. With current c2ops `codex@main`, the op still fails locally without Docker/Podman because the c2ops Codex implementation creates its own Shai runner before invoking Codex.

## Environment

- Date observed: 2026-05-14
- c2j: `github.com/colony-2/c2j v0.0.7-0.20260514020823-bfbbdf5d7254`
- c2ops `main`: `dea7b3201c7c48ff775bcc905e60c620ef4df866`
- Recipe: `codex-skill-execution-smoke.yaml`
- Command: `C2J_BIN=/tmp/c2j-main-bin/c2j recipe-tests/verify-codex-skill-execution-live.sh`

## Reproduction

1. Add `sandbox.type: none` under a c2ops Codex node's `inputs`.
2. Run the live Codex skill execution smoke in an environment without Docker/Podman:

```bash
C2J_BIN=/tmp/c2j-main-bin/c2j \
  recipe-tests/verify-codex-skill-execution-live.sh
```

## Expected

The Codex op should execute Codex without requiring Docker/Podman when sandboxing is disabled for the op.

## Actual

The op fails while constructing the c2ops Codex runner:

```text
codex.exec: codex error: codex did not produce structured output; create runner: failed to create docker client: unable to find docker socket; set DOCKER_HOST or ensure Docker/Podman is running
```

## Evidence

At c2ops `main` `dea7b3201c7c48ff775bcc905e60c620ef4df866`:

- `codex/op.yaml` does not expose a c2ops-level runner or sandbox mode input.
- `codex/pkg/codex/execute.go` constructs a `shai.SandboxConfig` and calls `opts.RunnerFactory(cfg)`.

The reserved c2j `sandbox` input is consumed by the extension wrapper and is not passed into the c2ops Codex payload, so c2ops has no way to switch to a direct host runner.

## Impact

TS-042 through TS-045 remain real live integration tests and correctly fail in local environments without Docker/Podman. They should not be skipped, but the dependency prevents the new unsandboxed Codex path from being validated until c2ops supports direct execution.

## Suggested Fix

Add a c2ops Codex runner mode that executes the Codex CLI directly on the host when sandboxing is disabled, while preserving the current Shai-backed mode for sandboxed execution.
