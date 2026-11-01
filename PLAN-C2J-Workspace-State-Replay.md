# Plan: carry workspace bases through task Git state

Status: implemented with task-local ref selection and a legacy history path.

Remove the per-operation `workspace-base` and per-hook `hook-base:*` tasks
introduced by `e7e2184` from the new execution path. Resolve a floating workspace
as part of preparing its existing task, and carry the result through the Git
properties already exchanged between steps. Existing histories containing those
tasks must continue to replay **and resume** correctly.

The reported job, `DUAJz6xo2nyPk4jEINSbvs8v99B`, demonstrates the compatibility
problem: an older interpreter expects `command_execution:command_execution`, but
chapter 6 is `workspace-base`. The current checkout replays that history; the
deployed Cortex story endpoint returned the mismatch. Removing the calls without
a compatibility path would make the corrected interpreter fail on that history
too.

1. **Keep observation separate from acceptance.**

   Repository identity and the symbolic ref describe where to start. The hash
   observed by a task attempt describes what it actually used. An accepted snapshot
   describes local work that subsequent operations must retain. Observing a hash
   does not itself accept a snapshot.

   Use the existing `ParentRef`, `ParentHash`, `PersistHash`,
   `ResolvedBaseHash`, and invocation-local `TaskBaseHash` properties rather than
   introducing a separate base-resolution result chapter. For the new mode,
   define and test the following handoff explicitly:

   | State or event | Task preparation and outgoing state |
   | --- | --- |
   | Floating workspace, no accepted candidate | Select any valid hash for the requested ref during worker preparation; hold it for this task attempt only. Consecutive tasks and retries may use different hashes. |
   | Successful const/no-change task | Retain the symbolic ref and leave `PersistHash` empty. Any reported observed hash is provenance, not a constraint on the next task or retry. |
   | First accepted modification | Return the resulting snapshot in `PersistHash` and the actual base used in `ParentHash`. Subsequent operations restore that candidate. |
   | Explicit successful rebase | Advance the active snapshot base to the rebase target and preserve the rebased HEAD. Subsequent tasks restore this new base/candidate pair. |
   | Failed task | Do not publish failed candidate edits as accepted parent workspace state. An unpinned retry may select another valid hash. |
   | Exact commit or empty workspace | Preserve the existing fixed-commit or empty-workspace semantics. |

   `ResolvedBaseHash` outside a running task is the last recorded observation,
   not a prediction of the next floating branch tip. Within a task attempt, the
   existing execution-base/head placeholders must resolve to its actual restored
   hashes. Audit Git operation defaults and template inputs for assumptions that
   the compiler has already resolved the next base.

   Preserve the current rebase behavior: `rebase`/`thinpackrebase` calls
   `SetWorkspaceBase(NewBaseHash)`, and the executor updates `ParentHash` and
   `ResolvedBaseHash` to the target while preserving the rebased HEAD. The diff
   from that active base to HEAD describes this patch's changes, excluding
   upstream changes incorporated by the rebase. Pinning prevents automatic
   movement with upstream; an explicit rebase deliberately advances the pinned
   base/candidate pair. `Workspace.InitialHash` remains historical entry
   provenance and is not the active patch base. No permanently fixed first-write
   origin or rebase behavior change is required.

2. **Move observation into existing worker preparation.**

   Update `workspace_controller.go` so a new-mode floating workspace can restore
   without a prepopulated `TaskBaseHash`. Resolve once, then use that exact commit
   consistently for restore, execution, snapshot creation, and output provenance.
   An advancing remote must never move a running task attempt's worktree.

   Update `op_executor.go` to return the observation on successful no-change
   results and available failure results where useful as provenance. Do not carry
   an observation as a pin between tasks, including internal tasks of one
   operation. Carry accepted snapshots through the existing Git-state handoff
   and preserve the existing scope acceptance boundaries.

   Keep network/Git resolution out of `DoTask` input builders: JobDB requires
   those builders to be deterministic and may call them while inspecting an
   unfinished execution. Completed replay must use recorded outcomes even if the
   remote has advanced or disappeared.

3. **Let unpinned retries select independently.**

   No durable observation handoff is required between unpinned attempts. A task
   may use A, fail, and retry on B. The same applies after worker replacement or
   a crash before an outcome is recorded. There is no requirement to contact the
   remote on every attempt merely to prove freshness: the contract is any valid
   hash for the requested ref, not a globally latest hash or a stable retry hash.

   Remove new-mode operation-wide observation caching. JobDB can continue to
   retry with the original task input: an unpinned input remains unpinned, while
   an input containing an accepted snapshot restores that snapshot. No JobDB
   retry API addition or extra durable preparation write is needed for this.

   Completed replay still consumes recorded outcomes; allowing a live retry to
   choose another hash does not authorize replay to rerun completed work.

   An explicit restart reconstructs state from its retained history prefix. A
   retained accepted write remains pinned; a discarded first write must not pin
   a restarted pre-write operation. Do not inherit observation caches from the
   discarded suffix.

4. **Use the hook's existing pin/load tasks.**

   The existing `hook:<invocation>:pin` task can observe the candidate and record
   its source-selection state. The existing `load` task must load the selected
   immutable source, including after a crash between pin and load. Preserve
   source integrity and trust/ancestry validation without using source selection
   to pin the workspace of every subsequent hook-body task.

   A read-only hook must not
   cause its enclosing workspace to stop following the branch. Accepted writes
   from the hook body still propagate through normal scope completion. Keep the
   trusted initial base and pinned recipe source distinct from each task's
   workspace observation. Audit any existing assumption that all unpinned tasks
   in a hook run at one candidate hash.

   Preserve the preexisting workspace-entry resolution and hook pin/load tasks;
   this change removes only the redundant per-operation/per-hook base tasks from
   the new path.

5. **Select compatibility behavior from durable state.**

   Preserve three execution modes rather than globally deleting the old calls:

   | Recorded workspace state | Corrected interpreter behavior |
   | --- | --- |
   | Pre-`e7e2184`, no ref policy | Preserve its historical pinned behavior and task sequence. |
   | `follow-until-change` from `e7e2184` | Preserve the existing `workspace-base`/`hook-base` path and its recorded observations. |
   | New task-preparation mode | Use existing task Git state; emit no per-operation/per-hook base task. |

   Discriminator: `follow-per-task-until-change` in the existing workspace
   `ref_policy` property for the new mode. Separate the semantic question
   “does this workspace follow its ref?” from the compatibility question
   “does this history use dedicated observation tasks?” This avoids guessing
   from executable versions or adding a new activity-output field that older
   strict decoders reject. This does not guarantee that an unchanged older
   interpreter can replay newer hook-state behavior.

   Choose the mode when the existing workspace-entry task records its result;
   inherit it with that workspace thereafter. A previously recorded entry keeps
   its mode across replay, resume, retries, and restart. A newly executed entry
   may choose the new mode. Already submitted snapshot workspaces retain their
   fixed incoming snapshot semantics.

   Legacy-mode continuations deliberately retain their old task sequence,
   including any necessary future base tasks. This avoids silently changing the
   contract of an unfinished job. Preserve legacy hook invocation counters too:
   the old `hook-base` call advances a workspace-ID counter, so omitting that
   bookkeeping can change later task identities even if hashes match.

   Do not delete chapters, swallow determinism errors, or relax task identity,
   command, scope, or contract-version checks. Keep contract version 1. Apply the
   same compatibility behavior to execution, CLI replay, and story generation.

6. **Prove both lifecycle behavior and replay compatibility.**

   Retain fixtures for pre-change histories and histories containing both kinds
   of base task. Include the reported job's relevant sequence. Test completed
   replay and unfinished continuation, including a history ending after a base
   task but before its operation, and after hook pin but before load.

   Add new-mode real-Git tests for read-only A → upstream B → fresh operation B;
   first write on A → later operation still on its accepted candidate; recorded
   failure at A followed by an unpinned retry at B; pinned retries restoring their
   accepted input; restart before and after an accepted write; rebase base/head
   propagation; exact refs; empty/nested workspaces; and multi-step operations
   whose unpinned tasks use different valid hashes.
   Exercise read-only and writing hooks with branch movement between phases.

   Assert that new-mode histories contain no `workspace-base` or `hook-base`
   tasks. Replay with Git/network unavailable and confirm that completed work is
   neither rerun nor refreshed. Exercise new histories with the pre-`e7e2184`
   interpreter as well: avoiding new tasks alone does not prove compatibility,
   especially because hook commands contain Git context. Identify any remaining
   command/output incompatibility before claiming older readers are supported.

   Re-run the workspace submission tests. `--from-workspace` must still capture
   the actual incoming task snapshot after restore; `--from-workspace-now` must
   still capture the live workspace without changing its HEAD/index.

7. **Document and deliver.**

   Update workspace lifecycle guidance and both copies of the c2j recipe skill
   references to explain observation versus acceptance, retry/restart behavior,
   and historical compatibility. Preserve the separate structured-rebase failure
   fixes from `e7e2184` and the workspace submission feature.

   Run the targeted Git-state, worker/compiler, replay, hook, child-broker, and
   workspace-submit checks. Re-run the reported job through the corrected replay
   reader. Deployment of that reader to Cortex is a separate necessary rollout
   step: changing the local CLI cannot repair Cortex's embedded interpreter.

   Existing buggy-format histories remain supported by the corrected reader;
   an unchanged older binary cannot retroactively understand their extra tasks.
   No history migration or job rerun is proposed.

Validation results: the corrected reader replays the reported job
`DUAJz6xo2nyPk4jEINSbvs8v99B`. Regression coverage includes interrupted legacy
base/pin tasks, offline replay, unpinned and pinned retries, multi-task operations,
hooks, and restart before/after accepted writes. A cross-version check against
`e8cd4f8` passes for a new ordinary-task history but rejects a new hook history
with a changed hook call descriptor. Upgrade replay consumers with the compiler;
forward compatibility with that unchanged older reader is not claimed.

The targeted regressions and both skill validations pass. The broader Git-state,
template, worker, workspace-submit, and broker run had two publication-recovery
timeouts at their existing 15-second deadline; both cases passed in isolation
(under one second combined). No other test failures were reported.
