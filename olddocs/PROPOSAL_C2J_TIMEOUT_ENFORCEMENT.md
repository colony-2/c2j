# Proposal: Enforce Existing c2j Timeouts End-to-End

## Context

The timeout bug should not be fixed by adding a separate cancellation system for
recipe tests. c2j already has timeout concepts in the recipe and op model:

- Recipe nodes have `timeout` in `recipe.NodeMetadata`.
- Ops have `OpMetadata.DefaultTimeout`.
- The compiler already converts op node `timeout` into `swf.RunPolicy.TotalTimeout`.
- `c2j test run --case-timeout` is a test harness timeout, but it should map onto
  the same recipe/op timeout enforcement path rather than becoming another
  executor.

The current failure is that these timeout concepts are not enforced consistently
all the way down to local passthrough execution and process cleanup.

## What Is Broken

The bug report is not evidence that every timeout path is dead. Some timeout
paths can still work today, especially when an op creates its own child context
and the command being run is a single immediate process. What is broken is the
end-to-end timeout path needed by `c2j test run --case-timeout` for passthrough
extension ops that spawn descendants.

The concrete broken chain is:

```text
c2j test run --case-timeout
  -> runPreparedCase creates tctx
    -> compiler.ExecuteRecipe is called without tctx
      -> compiler creates swf.RunPolicy only from explicit node timeout
        -> recipe-test passthrough ignores swf.RunPolicy
          -> extension op process receives no case deadline
            -> host process cancellation would only kill the immediate child
```

More specifically:

1. `--case-timeout` does not currently govern recipe execution.

   `runPreparedCase` creates `tctx := context.WithTimeout(...)`, but it does
   not pass that context into `compiler.ExecuteRecipe`. The timeout is only
   checked after `ExecuteRecipe` returns. If execution hangs inside an op,
   `runPreparedCase` never reaches the timeout check or result-writing path.

2. `swf.RunPolicy.TotalTimeout` is incomplete.

   `compiler.ExecuteOp` sets `RunPolicy.TotalTimeout` only when the recipe node
   has an explicit `timeout`. Existing op defaults, such as the
   `extension_execution` 30 minute default and `command_execution` 5 minute
   default, are not used as fallback task timeouts.

3. Recipe-test passthrough does not enforce the run policy it receives.

   `testJobContext.DoTask` receives `swf.RunPolicy`, but `doMockedTask` discards
   it. The passthrough branch invokes the real op without deriving a context
   from `RunPolicy.TotalTimeout`.

4. Composite timeouts are currently declarative only.

   `ExecuteSequence` computes a timeout and calls `executeCompositeInEnvelope`,
   but `executeCompositeInEnvelope` is a TODO that immediately calls the child
   function. Sequence/state/root timeout budgets therefore do not constrain
   nested work.

5. Host process cancellation is not process-tree cancellation.

   `process.ExecuteProcess` uses `exec.CommandContext`, which cancels the direct
   child process. If that child starts a wrapper, `node`, Codex, or another
   descendant, those descendants can keep running and keep pipes open. That
   explains the observed live tree:

   ```text
   c2j test run
     -> go run .
       -> extension wrapper
         -> node /usr/local/bin/codex
           -> native codex executable
   ```

## Did Recipe Timeout Work Before?

Based on the current code and git history, recipe timeout only appears to have
worked for root recipes that are a single op, or for op nodes with an explicit
`timeout`. In those cases, `compiler.ExecuteOp` converts
`NodeMetadata.Timeout` into `swf.RunPolicy.TotalTimeout`, and the normal SWF
runtime can enforce that run policy.

It does not look like recipe-level timeout ever worked for composite recipes in
this repository:

- Root `sequence` recipes pass root metadata into `ExecuteSequence`.
- `ExecuteSequence` computes `metadata.Timeout`.
- It then calls `executeCompositeInEnvelope`.
- `executeCompositeInEnvelope` has been a TODO since the initial repo history
  and simply runs `fn(ctx)`.

So a root recipe like this is parsed, but the root timeout is not enforced over
the sequence:

```yaml
id: example
version: "1.0"
timeout: 1m
sequence:
  - id: slow
    op: some_long_running_op
```

Timeout did work in narrower cases:

- `command_execution` has an input-level `timeout` field and wraps its own
  context before calling `process.ExecuteProcess`.
- Extension manifests can declare `timeout`, and the extension op wraps the
  context before process execution.
- A root recipe whose body is a single `op` can pass its root `timeout` through
  `ExecuteOp` into `swf.RunPolicy.TotalTimeout`.
- Child op nodes with explicit `timeout` also pass that timeout into
  `swf.RunPolicy.TotalTimeout`.
- For a simple command that does not spawn descendants, `exec.CommandContext`
  can make the immediate child exit and return a timeout-looking error.

Those cases do not prove the full case timeout path is working. The reported
failure uses a different path: the test harness case timeout is not connected to
the recipe/op run policy, local passthrough discards the run policy, and the
extension process tree includes descendants that are not killed by
`exec.CommandContext`.

So the likely answer is: op timeout worked; root/composite recipe timeout did
not. The selector-backed passthrough extension bug exposed that gap because the
test harness timeout was treated like a side context instead of being mapped
into the same run policy path.

## Current Gaps

1. `--case-timeout` is only checked after `ExecuteRecipe` returns.

   `pkg/recipetest/harness.go` creates a timeout context, but `ExecuteRecipe`
   is not governed by it. If an op never returns, the timeout check is never
   reached.

2. Op default timeouts are not used as the fallback task timeout.

   `extension_execution` declares a `DefaultTimeout` of 30 minutes and
   `command_execution` declares 5 minutes, but `compiler.ExecuteOp` currently
   only sets `RunPolicy.TotalTimeout` when the recipe node has an explicit
   `timeout`.

3. Composite recipe timeouts are stubbed.

   `ExecuteSequence` computes a timeout, but `executeCompositeInEnvelope`
   currently just calls the function. State machine/root recipe timeout behavior
   has the same underlying issue: composite execution does not create an
   enforced timeout budget for nested work.

4. Recipe-test passthrough ignores `RunPolicy.TotalTimeout`.

   The compiler passes `RunPolicy` into `DoTask`, but the test harness
   passthrough path invokes the op directly and does not enforce the policy.

5. Process cancellation is not process-tree cancellation.

   `process.ExecuteProcess` uses `exec.CommandContext` for host execution. That
   is not enough for commands that spawn children, such as:

   ```text
   go run .
     -> extension wrapper
       -> node /usr/local/bin/codex
         -> native codex executable
   ```

   Killing only the immediate child can leave descendants alive and can keep
   pipes open, causing the caller to keep waiting.

## Design Principle

There should be one timeout model:

```text
recipe/case timeout budget
  -> node timeout
    -> op default timeout
      -> swf.RunPolicy.TotalTimeout
        -> local passthrough context deadline
          -> process tree termination
```

`--case-timeout` should be treated as a root recipe timeout overlay for the test
case, not as an independent watchdog with separate semantics.

## Workstream 1: Execution Timeouts

Goal: make op and recipe execution timeouts work correctly without involving
the recipe-test framework.

This workstream should make the author-facing execution model true:

```text
recipe/root timeout
  -> sequence/state timeout
    -> op node timeout
      -> op metadata default timeout
        -> swf.RunPolicy.TotalTimeout
          -> op context deadline
            -> process tree termination
```

### Scope

- Apply explicit op node `timeout` consistently.
- Apply `OpMetadata.DefaultTimeout` when an op node has no explicit timeout.
- Enforce root recipe timeout for single-op, sequence, and state-machine roots.
- Enforce nested sequence and state-machine timeout budgets.
- Clamp nested op task timeouts to the remaining parent recipe/sequence/state
  budget.
- Ensure host process cancellation terminates the full process tree.
- Keep extension manifest timeout as a nested timeout inside the op execution
  deadline.
- Return timeout errors that callers can identify as timeout failures.

Out of scope for this workstream:

- `c2j test run --case-timeout`.
- Test case result normalization.
- Test output artifact durability.

### Proposed Behavior

In `compiler.ExecuteOp`, compute an effective task timeout:

```text
explicit node timeout
else op metadata DefaultTimeout
else no op-specific timeout
```

Then clamp that timeout to any active parent recipe/sequence/state deadline and
set `swf.RunPolicy.TotalTimeout` from the result.

For composite nodes, finish `executeCompositeInEnvelope` so sequence and
state-machine timeouts are real. The compiler should carry an internal
execution scope with the active deadline; it should not hang ad hoc timeout
state off the public `workflow.Context`.

Nested timeout composition should be shortest-deadline-wins:

```text
effective task timeout = min(node/op timeout, remaining composite budget)
extension process deadline = min(task RunPolicy timeout, manifest timeout)
```

For host process execution, replace plain `exec.CommandContext` cancellation
with process-tree cleanup.

Unix behavior:

- Start the command in a new process group using `SysProcAttr.Setpgid = true`.
- Wait for either process exit or context cancellation.
- On cancellation, send `SIGTERM` to `-pid` (the process group).
- Wait a short grace period.
- If still running, send `SIGKILL` to `-pid`.
- Always wait for `cmd.Wait()` to finish so pipes are closed.

Windows behavior:

- Start the child normally.
- On cancellation, run `taskkill /T /F /PID <pid>`.
- Wait for command cleanup.

This should live in `pkg/ops/process`, so command ops, extension ops, and CEL
extension functions all benefit from the same process cleanup behavior.

### Implementation Steps

1. Add a helper to compute effective op timeout from explicit node timeout,
   `OpMetadata.DefaultTimeout`, or no timeout.
2. Add an internal compiler execution scope that carries active timeout
   deadlines for recipe, sequence, and state-machine execution.
3. Apply the effective timeout helper in `ExecuteOp` and clamp it to the active
   execution scope before creating `swf.RunPolicy`.
4. Implement `executeCompositeInEnvelope` for sequence and state-machine
   timeout budgets.
5. Ensure root recipe metadata enters the same timeout scope for root op,
   sequence, and state-machine recipes.
6. Replace host process cancellation with process-group/process-tree cleanup.
7. Normalize execution-layer timeout errors enough that callers can reliably
   detect them.
8. Add focused regression tests.

### Regression Coverage

- Op without explicit timeout uses `OpMetadata.DefaultTimeout`.
- Explicit node timeout overrides op default timeout.
- Root single-op recipe timeout becomes `RunPolicy.TotalTimeout`.
- Root sequence timeout fails even if child work would otherwise run longer.
- Root state-machine timeout fails across transitions.
- Nested op timeout is clamped by parent sequence/state-machine/root timeout.
- Selector-backed extension with a short manifest timeout returns promptly.
- Extension timeout and recipe node timeout compose by shortest deadline.
- Host command that spawns a child is fully killed on timeout.
- Descendant process does not write a delayed marker after timeout.
- Stdout/stderr pipes close and `ExecuteProcess` returns promptly.
- Windows process-tree cleanup is covered where feasible.

### Definition Of Done

- Execution timeouts work without relying on recipe-test-specific code.
- Composite recipe timeouts are enforced, not just parsed.
- Process cancellation does not leave descendants running.
- Timeout failures are distinguishable by callers.

## Workstream 2: Test Framework Timeout Overlay

Goal: make recipe-test timeouts reuse the execution timeout machinery from
Workstream 1 instead of acting as a separate watchdog.

This workstream depends on Workstream 1. The test framework should pass or
overlay timeouts into normal execution; it should not be responsible for
inventing separate op cancellation semantics.

### Scope

- Map `c2j test run --case-timeout` onto root recipe timeout semantics.
- Ensure test passthrough execution honors `swf.RunPolicy.TotalTimeout`.
- Normalize timed-out cases to test result status `timed_out`.
- Ensure timeout failures return to the normal result-writing path.

Out of scope for this workstream:

- Defining new timeout semantics.
- Killing process trees directly from the test harness.
- Changing author-facing recipe timeout behavior beyond the overlay.

### Proposed Behavior

For `c2j test run`, clone the loaded recipe before execution and overlay the
case timeout onto the root node metadata:

- If the recipe root already has a timeout, use the shorter of the recipe root
  timeout and `--case-timeout`.
- If the recipe root has no timeout, set it to `--case-timeout`.

This makes `--case-timeout` the maximum root recipe runtime for that test case.
It also keeps test behavior aligned with author-facing recipe semantics.

`testJobContext.DoTask` already receives `swf.RunPolicy`. In passthrough modes,
the recipe-test job context should act like a local SWF task runtime:

- In `testJobContext.doMockedTask`, preserve the incoming `runPolicy`.
- When mode is `passthrough` or `record_passthrough`, derive a local
  `context.Context` from `runPolicy.TotalTimeout`.
- Pass that context into `runPassthroughTask`.
- Invoke the op with that context instead of `context.Background()`.

Once passthrough honors `RunPolicy` and Workstream 1 handles process cleanup,
timed-out cases should return to `RunCase`, allowing the existing result-writing
path to run.

`RunCase` should normalize timeout errors into:

- `status: timed_out`
- `failure_category: timeout`
- a message including case id, node path, op, and timeout duration when known

The CLI can continue writing results after `runCases` returns, but a follow-up
hardening step should write each case result as it arrives so completed results
are durable even if a later case fails catastrophically.

### Implementation Steps

1. Add a recipe clone/overlay helper for root timeout metadata.
2. Change `--case-timeout` handling to apply that helper before
   `compiler.ExecuteRecipe`.
3. Stop treating the current `tctx` as post-hoc proof of timeout. It can remain
   useful for bounded artifact collection, but execution timeout must come from
   the overlaid recipe timeout.
4. Change `testJobContext.doMockedTask` to accept and preserve
   `swf.RunPolicy`.
5. Derive passthrough op invocation context from `RunPolicy.TotalTimeout`.
6. Pass that context through `runPassthroughTask`.
7. Normalize timeout errors into `timed_out` test results.
8. Add recipe-test and CLI regression tests.

### Regression Coverage

- `--case-timeout` overlays root recipe timeout on a cloned recipe.
- Existing shorter recipe root timeout wins over longer `--case-timeout`.
- Shorter `--case-timeout` wins over longer recipe root timeout.
- Passthrough op receives a context that is canceled at
  `RunPolicy.TotalTimeout`.
- Timed-out passthrough case returns `status: timed_out`.
- Timeout result includes the active node path and op name when available.
- `c2j test run --case-timeout` returns non-zero for a timed-out case.
- Run artifacts are written under `--out-dir`.
- The recorded case status is `timed_out`, not a generic failure.

### Definition Of Done

- The test framework does not own independent timeout semantics.
- Case timeout works by overlaying root recipe timeout.
- Passthrough mode behaves like a local implementation of
  `swf.RunPolicy.TotalTimeout`.
- Timeout cases produce durable test results.

## Risks And Mitigations

- **Risk: killing process groups may terminate unrelated processes.**
  Mitigation: only kill the process group created for the op process.

- **Risk: composite timeout enforcement can leave goroutines running if it is
  implemented as a goroutine race without task cancellation.**
  Mitigation: do not rely only on goroutine racing. Composite timeout must clamp
  nested `RunPolicy.TotalTimeout`, so blocking task execution receives the
  deadline at the task boundary.

- **Risk: default op timeouts may change behavior for recipes that relied on
  unlimited execution.**
  Mitigation: this is intended behavior because op metadata already declares
  defaults. Authors can set explicit node timeouts for longer-running ops.

- **Risk: remote SWF runtime and local passthrough runtime may diverge.**
  Mitigation: make `RunPolicy.TotalTimeout` the contract and test both paths
  where possible.

## Recommendation

Use the existing timeout model as the single source of truth and deliver the fix
as two ordered pieces of work:

1. **Execution timeouts:** make op defaults, explicit op timeouts, root recipe
   timeouts, composite recipe timeouts, and process-tree cancellation work
   without any recipe-test-specific behavior.
2. **Test framework timeout overlay:** map `--case-timeout` onto root recipe
   timeout, enforce `RunPolicy.TotalTimeout` in local passthrough, and normalize
   timed-out test cases.

This preserves one timeout model while keeping the core execution fix separate
from the test harness integration.
