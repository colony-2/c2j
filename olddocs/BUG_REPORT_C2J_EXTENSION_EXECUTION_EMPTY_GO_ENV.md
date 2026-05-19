# Bug Report: c2j extension execution drops Go module cache environment

## Summary

`c2j submit --recipe-file ... --run --embed` can resolve c2ops selectors at `@main`, but live `extension_execution` fails before the c2ops `codex` op starts because the extension process is launched without `GOPATH` or `GOMODCACHE`.

Observed failure:

```text
extension op "git+https://github.com/colony-2/c2ops.git//codex@dea7b3201c7c48ff775bcc905e60c620ef4df866" failed: exit status 1; stderr: go: module cache not found: neither GOMODCACHE nor GOPATH is set
```

## Environment

- Date observed: 2026-05-14
- c2j: `github.com/colony-2/c2j v0.0.6`
- c2ops selector in recipe: `git+https://github.com/colony-2/c2ops.git//codex@main`
- c2ops `main` resolved by c2j to `dea7b3201c7c48ff775bcc905e60c620ef4df866`

## Reproduction

1. Create a temporary git cell with a `main` commit.
2. Run a live recipe through embedded c2j:

```bash
C2J_BIN=/tmp/c2j-v006-bin/c2j \
  recipe-tests/verify-codex-skill-execution-live.sh
```

The script compiles the scenario, creates the temp cell repo, then runs:

```bash
c2j submit \
  --cell /tmp/codex-skill-execution-live/cell-repo \
  --recipe-file ./codex-skill-execution-smoke.yaml \
  --run \
  --embed
```

## Expected

The c2ops `codex` extension should execute with a usable Go environment and then run the live Codex + skill workflow.

## Actual

The recipe reaches the c2ops `codex` extension step, then fails before Codex starts:

```text
go: module cache not found: neither GOMODCACHE nor GOPATH is set
```

## Likely Cause

In c2j `v0.0.6`, `pkg/ops/extensions/runtime.go` sets the extension process environment from only the extension spec env:

```go
cmd.Env = buildProcessEnv(req.Env)
```

That drops the parent process environment. For c2ops extensions that run Go, an empty environment leaves `go` without a module cache location.

## Impact

Live integration tests TS-044 and TS-045 fail before exercising Codex or skill execution. The deterministic mocked c2j suites still pass because they mock `extension_execution`.

## Suggested Fix

Merge the parent process environment with extension spec env, letting extension spec keys override parent keys. At minimum, preserve `HOME`, `PATH`, `GOPATH`, and `GOMODCACHE` for extension processes that invoke Go.
