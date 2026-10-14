# Restarting recipe scopes

Recipe jobs use JobDB execution scopes for recipe, sequence, state-entry, and op
boundaries. A sequence or state body containing several tasks is one restartable
block. The story API lists recorded restart targets in `restart_scopes`; each
entry includes the logical `scope_id` and its start time.

Call `starter.RestartRecipeJob` with the original job key, the selected scope ID,
and an optional `task.ContextPatch`. JobDB creates a new job that retains the
prefix before that scope and runs the scope and continuation again. Physical
chapter ordinals are storage coordinates, not restart targets.

The patch is applied by the scope's `OnRestart` hook before its inputs, conditions,
and child task commands are evaluated. It can merge job context and patch scoped
outputs/artifacts using the existing context-patch schema. The hook validates the
payload and always starts from a fresh copy of the original scope-entry context,
so resuming or replaying the restarted job applies the patch consistently.

Completed prefix tasks replay from their outcomes without resolving inputs or
running preparation again. A block retry likewise starts from fresh block-entry
state and creates new child invocations; ordinary suspension retains completed
children. JobDB owns timeout budgets, catch decisions, retries, and cancellation.

This format is for fresh execution-scope histories. Old step-offset restarts,
context-patch task outcomes, and intentional input-hash mismatches are removed.

Coverage: `pkg/worker/compiler/execution_scopes_integration_test.go` exercises
nested-block restart with a retained prefix, patched continuation, read-only
replay, retries, catches, and interruption recovery against SQLite and HTTP.

Native op implementations can read `deps.JobTool().InvocationID()` for an opaque
logical task ID. Combine it with the tenant/job key for idempotent external work.
It survives task retries and unfinished-task recovery; a block or whole-job retry
gets a new ID. Native child submission follows this rule. Physical task ordinals
and recipe node paths must not be used as side-effect identities. Custom JobTool
implementations must supply this method. Child-job discovery uses a hash of that
task identity; multi-step ops combine the children from their recorded outcomes.
