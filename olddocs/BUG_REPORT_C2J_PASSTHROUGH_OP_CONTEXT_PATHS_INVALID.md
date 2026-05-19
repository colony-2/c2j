# Bug Report: passthrough recipe tests provide an invalid op context path layout

## Summary

`c2j test run` with passthrough mocks does not provide the same operation
filesystem contract as normal op execution.

Three related failures block live Codex + skill integration tests:

1. The passthrough `context.environment.op.worktree_path` directory does not
   exist before the first passthrough op.
2. The passthrough worktree and workdir are siblings, so c2ops Codex rejects the
   worktree as escaping the workdir.
3. Files written to `context.environment.op.outbox` by passthrough
   `command_execution` are not available as `sequence.<node>.artifacts[...]`.

This prevents live recipe tests from exercising selector-backed Codex ops even
when sandboxing is disabled.

## Environment

- Date detected: 2026-05-15 at 23:24:08 UTC
- Reproduced: 2026-05-15 at 23:27:00 UTC
- c2j checkout: `/c2j`
- c2j commit: `bf010f0` (`Fix validation fallback for safe CEL helpers`)
- c2ops checkout observed: `/c2ops`
- c2ops commit observed: `9d02c2e` (`remove shai from codex op, rely on extension framework.`)
- Recipe: `codex-skill-execution-smoke.yaml`
- Suite: `recipe-tests/codex-skill-execution-smoke.scenario.md`

## Reproduction

Run the live smoke with explicit passthrough mocks for `command_execution`, the
remote Codex selector, and `extension_execution`:

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

With a first passthrough `command_execution` op using its default working
directory, the run fails immediately:

```text
command execution failed: chdir /tmp/c2j-test-work-.../worktree: no such file or directory
```

After explicitly seeding that directory, the run reaches c2ops Codex and fails:

```text
codex.exec: codex execution error: codex: worktree root
"/tmp/c2j-test-work-.../worktree" escapes workdir
"/tmp/c2j-test-work-.../workdir"
```

With the normal outbox-to-artifact contract, passthrough `command_execution`
also fails to expose seeded outbox files to the next op:

```text
failed to resolve templates op artifacts: failed to resolve input
'outcome/plan.json': failed to evaluate CEL expression: no such key:
outcome/plan.json
```

## Expected Behavior

Passthrough recipe tests should match the normal op execution context contract:

- `context.environment.op.workdir` exists before the op runs.
- `context.environment.op.worktree_path` exists before the op runs.
- `context.environment.op.inbox` exists before the op runs.
- `context.environment.op.outbox` exists before the op runs.
- Files written under `context.environment.op.outbox` are collected as output
  artifacts, the same as normal op execution.
- For direct/no-sandbox execution, op-visible paths should be usable by the
  running op process.
- The worktree should not be outside the workdir layout expected by
  selector-backed extension ops such as c2ops Codex. A typical valid layout is:

```text
<case-root>/
  workdir/
    worktree/
    inbox/
    outbox/
```

or the same layout used by the regular op executor.

## Actual Behavior

The recipe-test harness creates the passthrough run context with sibling paths:

```text
<case-root>/worktree
<case-root>/workdir
<case-root>/inbox
<case-root>/outbox
```

The directories are not all created before the first passthrough op, c2ops
Codex rejects the sibling worktree/workdir layout, and passthrough outbox files
are not collected as recipe artifacts.

## Impact

- Blocks live `c2j test run` coverage for Codex + skills.
- Forces smoke recipes to add test-only worktree seeding.
- Makes `context.environment.op.worktree_path` unreliable in passthrough tests
  even though it is the documented path for op processes.
- Makes the standard outbox-to-artifact recipe pattern untestable with
  passthrough command ops.
- Hides whether the latest c2ops Codex selector works end-to-end, because the
  test harness fails before Codex can do useful work.

## Suggested Fix

Build passthrough test case roots with the same directory layout and creation
semantics as normal op execution.

Regression coverage should include a recipe-test passthrough case that:

1. Runs `command_execution` with default `working_directory`.
2. Asserts `context.environment.op.worktree_path` exists.
3. Writes `context.environment.op.outbox/results/status.json` and asserts it is
   visible as `sequence.<node>.artifacts["results/status.json"]`.
4. Runs a selector-backed extension op with `sandbox.type: none`.
5. Asserts the extension sees a worktree path that does not escape its workdir.
