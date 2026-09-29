# Recipe node workspaces in another cell

Status: implemented, 2026-09-29. See [GUIDE-Node-Workspaces.md](GUIDE-Node-Workspaces.md) for the supported API and [FEATURE-ANNOUNCEMENT-Node-Workspaces.md](FEATURE-ANNOUNCEMENT-Node-Workspaces.md) for the announcement.

Implementation notes: workspace state uses a shared Git-base pointer and commit pointer in `ResolutionContext`, plus a lexical overlay that preserves output paths. Cell resolution is factored into `pkg/cellref`, and the worker uses the existing selector cache to pin the base. Explicit child submissions carry an initial commit and restore-artifact reference, resolved by the task worker. Deployment requires upgrading compiler/task-worker fleets together; this change does not add a fleet-wide capability negotiation service.

A job owned by cellA should be able to execute a node against cellB's repository in a separate, writable workspace. The existing restore, snapshot, thin-pack, diff, retry, and replay mechanisms continue to apply. When the node finishes, execution resumes against cellA's existing workspace state. Results and artifacts can cross the boundary; workspace changes do not cross it implicitly.

“Ephemeral” describes the workspace's relationship to the target cell. Its changes remain durable within the job's execution history, but entering or leaving the workspace never publishes them to cellB. Explicit merge/push operations remain possible under existing external controls. This feature does not introduce a read-only mode or an additional merge approval mechanism.

## Proposed recipe API

Add one optional `workspace` field to `NodeMetadata`:

```yaml
workspace:
  cell: cellB
  ref: main              # optional; defaults using cell resolution
```

Both values accept literal strings or existing template expressions. `cell` is required when the object is present. Empty values, unknown keys, and non-string resolved values are errors; omitting `workspace` inherits the current workspace. There is no scalar shorthand initially.

The object form distinguishes workspace selection from child-job `cell_name`, and gives ref selection a natural home. It applies to root and nested `op`, `sequence`, `state`, and `child_group` nodes, individual entries in `state.states`, and `include` call sites. Shared definitions can contain it. A `shared` reference itself currently has no `NodeMetadata`; put the field on the definition or a containing sequence instead of adding reference overrides in this feature.

Example, submitted as a job in cellA:

```yaml
id: inspect-dependency
version: "1.0.0"
input_schema:
  dependency:
    type: string
    default_value: cellB
inputs:
  dependency: "{{ inputs.dependency }}"
sequence:
  - id: prepare_a
    op: command_execution
    inputs:
      working_directory: "{{ context.environment.op.worktree_path }}"
      run: "printf 'cellA work\n' > a.txt"

  - id: inspect_b
    workspace:
      cell: "{{ inputs.dependency }}"
    sequence:
      - id: prepare
        op: command_execution
        inputs:
          working_directory: "{{ context.environment.op.worktree_path }}"
          run: "printf 'temporary analysis\n' > analysis.txt"
      - id: report
        op: command_execution
        inputs:
          working_directory: "{{ context.environment.op.worktree_path }}"
          run: |
            cat analysis.txt
            cp analysis.txt "${{ context.environment.op.outbox }}/report.txt"
    outputs:
      report: "{{ sequence.report.outputs.stdout }}"

  - id: resume_a
    op: command_execution
    inputs:
      working_directory: "{{ context.environment.op.worktree_path }}"
      run: "cat a.txt; test ! -e analysis.txt"
outputs:
  report: "{{ sequence.inspect_b.outputs.report }}"
```

The two cellB operations share a logical workspace through snapshots. They need not run on the same worker or reuse a physical checkout.

## Scope and lifetime

The confirmed behavior is a fresh workspace per explicit node invocation, shared by its descendants. Cell selectors support literals and runtime templates. The detailed scope rules are:

| Situation | Behavior |
| --- | --- |
| Node omits `workspace` | Inherit its enclosing workspace and current snapshot. |
| Node declares `workspace` | Create an independent workspace for that node invocation, starting from the target ref resolved at entry. |
| Descendants omit `workspace` | Share the enclosing node's workspace state. |
| Two sibling nodes select cellB | Independent workspaces; the second does not inherit the first's changes. |
| Nested selection of cellC inside cellB | Create cellC state, then resume cellB state on exit. |
| Explicit selection of the current cell | Still create an independent workspace, even if repo and ref are identical. |
| Explicit selection of cellA inside cellB | Start a new cellA workspace; do not reconnect to the suspended outer cellA workspace. |
| State machine declares the field | Its states and loops share that workspace unless overridden. |
| Individual state declares the field | Each distinct state entry gets a fresh workspace; replay of that entry restores the same one. |
| Node succeeds, fails, times out, or routes to another state | Exit the boundary without advancing the enclosing workspace. |

Sharing cellB changes across several operations is expressed by placing those operations in a sequence or state machine with one declaration. There is no job-wide registry keyed only by cell name.

A workspace boundary belongs to a logical invocation, not a task attempt. Worker retries and workflow replay reuse its pinned target and snapshot history. A composite retry retains the same boundary and follows existing retry semantics for successful inner task state; it does not resolve a new target ref on every attempt. Failed task snapshots remain diagnostic unless the existing runtime explicitly accepts that task's result. The feature must not introduce an implicit “commit on failure” policy.

Logical state is discarded from active execution when the boundary closes. Snapshot artifacts remain under existing job retention rules. Physical work directories use existing cleanup behavior.

## Separate job identity, workspace identity, and recipe source

The implementation must represent these independently:

| Surface inside a cellB workspace | Value |
| --- | --- |
| Job key, tenant, project, job metadata, scheduling ownership | Original job in cellA. |
| `context.workflow.cell` / `cell_id` | Original job's cellA identity. |
| New `context.workspace.cell` | cellB. |
| New `context.workspace.scope_id` | Stable ID of this workspace invocation. |
| `context.git.*` | Active cellB repository, ref, author, base, and current commit state. |
| `context.environment.*` and op-visible paths | Current task's cellB checkout and normal inbox/outbox. |
| `context.recipe_source.*`, includes, pinned extension code | Existing recipe source and inclusion semantics. |
| Existing `C2J_CURRENT_*` job provenance | Existing owning-job/recipe provenance. |
| New `C2J_WORKSPACE_CELL_NAME`, `C2J_WORKSPACE_SCOPE_ID` | Active workspace identity. |

Expose workspace repo/ref/hash through `context.git` rather than duplicating their mutable values in multiple template objects. Populate `context.workspace.cell` with the owning cell outside overrides, so new recipes can consistently use it for workspace-specific behavior.

Do not overwrite the existing serialized `GlobalGitTaskContext.CellName`: it is also consumed by `currentJobContext`, parent-child provenance, and environment injection. Add explicit workspace identity fields and provide active-workspace accessors for Git operations and fallback commit authors. Retain an explicitly configured author; otherwise use the active cell's normal author fallback.

Workspace selection does not reload the recipe, implicitly select cellB's execution image, change `execution_needs`, or retarget relative extension selectors. Commands intentionally read files from cellB. Includes and extension selectors continue using the recipe's pinned code source. Existing explicit execution requirements remain available when cellB work needs a different image or resources.

## What needs to change in the current implementation

The existing worker already provides most of the execution machinery:

| Current code | Relevant behavior / required change |
| --- | --- |
| `pkg/recipe/node.go`, `recipe.go`, `state.go` | Common metadata reaches roots and state entries; add parsing, validation, schema, and round-trip coverage. |
| `pkg/template/template_resolver.go` | `NewChildContext` shares one `*GitCommitContext`. Introduce sharing within a workspace and isolation across boundaries. |
| `pkg/worker/compiler/artifact_job_context.go` | `thinpackForwarder.lastThinpack` is job-wide. Forward snapshots by workspace scope instead. |
| `pkg/template/context_patch.go` | Job patches currently apply to all ancestors. Git patches must stop at a workspace boundary. |
| `pkg/worker/ops/op_executor.go` | Creates a temporary checkout, restores it, runs the op, and calls `PersistWithDiffs`. Reuse this lifecycle with the active workspace descriptor. |
| `pkg/git/gitstate/workspace_controller.go` | Ref-mode restore follows the current ref tip; add an explicitly pinned initial-base path for contextualized workspaces. |
| `pkg/recipejob/target.go`, `pkg/config/config.go` | Existing repository normalization, cell expansion, and ref defaults are reusable, after removing the compiler dependency from shared resolution code. |
| `pkg/worker/compiler/inline_resolution.go` | Includes become wrapper sequences; preserve workspace declarations and enter each declared boundary exactly once. |

These observations use the working tree as inspected, including current timeout/recovery changes. Implementation should integrate with those changes rather than replace them.

## Workspace state model

Introduce a runtime workspace object owned by a boundary and shared by its descendants. Conceptually:

```go
type WorkspaceState struct {
    Descriptor WorkspaceDescriptor
    GitBase    contextual.GitBaseContext
    Commit     contextual.GitCommitContext
}

type WorkspaceDescriptor struct {
    ScopeID       string
    ParentScopeID string
    CellName      string
    Repository    string
    RequestedRef  string
    InitialHash   string
}
```

This is a proposed shape, not a new persisted database entity. Keep descriptors and commit/artifact references serializable in ordinary task inputs/outputs; reconstruct in-memory sharing during replay. No worker-local path belongs in the durable descriptor.

`InitialHash` records the immutable entry point. `GitBase` and `Commit` hold the active state, including legitimate rebase/context-patch updates. Update template context from the shared workspace state so same-workspace descendants cannot observe stale copies of the Git base.

The existing job workspace is the root state. An absent scope field in legacy task data means that root, preserving existing serialized inputs and replay identities. New boundary IDs derive from the job identity and a deterministic boundary-entry identity: node path plus logical entry ordinal. Do not use random `CurrentRunID`, timestamps, local directory names, cell names alone, or per-task attempt numbers. Nested boundary IDs include the parent scope ID. Allocate the entry ordinal once outside the node's retry envelope.

Keep workspace ancestry separate from template/output ancestry. An isolated node must still publish results to its original `sequence.<id>` or `states.<state>` location. Avoid adding a visible synthetic sequence that changes invocation paths or output lookup rules.

## Resolving and entering a workspace

Resolve cell names relative to a stable resolution configuration for the owning job. Do not read whichever `.c2j/config.yaml` happens to be in the worker process's current directory, or switch naming rules when entering cellB.

1. Capture a portable resolution context when preparing the job: the effective naming pattern, self/root repositories and refs, and explicit configuration values needed by the existing resolver. Resolve local auto-detection at this point. API submitters must provide an equivalent context or use an explicit repository target. Do not put credentials in the snapshot.
2. At boundary entry, evaluate `workspace.cell` and optional `workspace.ref` against the incoming template context. Parent inputs, prior visible outputs, transition data, and inherited vars are available. The node's own vars are evaluated after entry and cannot define its selector. Composite input bindings keep their current caller-side evaluation semantics.
3. Execute an internal durable task, proposed name `recipe_workspace_resolve`, to normalize the target, select its ref, and resolve the ref to a full commit hash. Record the result before any target operation executes. Ordinary replay reuses this result without resolving the branch again.
4. Create a new workspace state with the resolved base and no inherited commit or thin pack. Enter it before resolving node vars, op defaults, op inputs, artifact bindings, or execution requirements that should see the target context.
5. Execute the node normally. Publish outputs/artifacts using normal scope rules. Returning to the enclosing context resumes its existing workspace state.

Use current cell input conventions: configured names, `root`, and explicit repository locations. An omitted ref uses the existing target resolver's defaults (configured self/root ref where applicable; currently `main` for other repositories), not cellA's active branch. An explicit `ref` overrides that choice. This does not imply a new registry that discovers every cell's preferred branch.

Factor reusable resolution into a package below both `recipejob` and `worker/compiler`; importing `recipejob` directly from the compiler would create an import cycle today. Use `internal/repository` normalization. Local repository sources retain existing local/embedded limitations and must be accessible to the executing worker; portable execution uses portable repository locations.

Do not equate `CanSubmitToCell` with permission to read a workspace. This feature submits no job to cellB merely by entering it. Resolve and access the repository using existing runtime credentials and external access controls. Resolution or access failure is a normal node failure, without fallback to cellA.

Apply existing timeouts, cancellation, task admission, and execution handoff to the resolver. It is an internal task that receives no workspace thin pack and produces none. Validation checks selector expression shape/types, uses placeholders for runtime-dependent targets, and performs no repository access just to validate a recipe.

For a node skipped by existing control-flow rules, do not resolve its target. Do not broaden node `when` behavior as part of this work; verify supported skip paths explicitly, since the presence of `NodeMetadata.When` alone does not establish that every executor evaluates it.

## Snapshot and artifact isolation

Two changes are mandatory; changing `context.git.repo` alone is insufficient.

**Scope the commit pointer.** An override creates a new `WorkspaceState`; an ordinary child shares its parent's state. `UpdateGitState` updates only that state. On completion, failure, or catch routing, there is no Git-state merge into the enclosing context.

**Scope automatic thin-pack forwarding.** Replace the single `lastThinpack` with a map keyed by workspace scope, or an equivalent explicit snapshot reference carried with each workspace. A task can consume/update only its own scope's entry. Internal resolver and timeout checkpoint tasks bypass forwarding. Keying only by repo or cell would incorrectly share state between independent invocations.

Carry the workspace scope ID in activity request/result metadata and validate that a result matches the submitted scope. Reject conflicting automatic restore artifacts. Select the restore pack from the workspace's explicit snapshot reference; never infer the active workspace from the last arbitrary artifact named `thin-pack`.

User-bound artifacts, including a deliberately exported foreign snapshot, are data and must not replace the restore pack. Distinguish the internal restore reference from normal inbox/artifact bindings. Task-local thin-pack and diff artifact names can remain unchanged because their stored keys include job/task identity.

Fix job-result assembly as well as next-task forwarding: if the last executed node is in cellB, `GetLastArtifacts()` must not make cellB's snapshot appear to be the owning job's cellA snapshot. Assemble any implicit primary Git result from the root workspace. Preserve normal user result artifacts, and retain foreign snapshots with explicit workspace provenance in task history. This rule also applies when the recipe root declares a workspace: its data outputs return normally, while its Git state stays isolated from the submitted root workspace. No new public “adopt workspace changes” API is included.

### Pin the initial base without requiring a thin pack

Today `Restore` follows `BaseRef` when `PersistHash` is empty, and cloning can overwrite `ResolvedBaseHash`. Merely setting `resolved_hash` is therefore insufficient to pin cellB.

Add an explicit pinned-base restoration mode for workspace descriptors. The initial checkout must fetch/checkout `InitialHash`, preserve the requested ref for context/defaults, and require no thin pack. Subsequent tasks restore their accepted snapshot using existing persistence mechanics. A const or no-change task remains pinned even when it has no `PersistHash`; it must never regress to following the branch tip. Explicit Git operations may advance/rebase the workspace's active base without rewriting its entry descriptor.

Legacy tasks without the new descriptor keep existing ref-following behavior. If a pinned commit becomes unavailable, fail the node instead of silently using a newer commit.

## Context patches, catches, and compiler entry points

Partition job-context patches by field before applying them. Git base/ref/hash/author updates apply only within the active workspace and stop at its boundary. Existing unrelated job-global patches keep their established behavior; apply them as field patches so propagation cannot copy a cellB Git base into cellA as a side effect. Workspace IDs and owner identity cannot be reassigned through a workspace Git patch.

Catch evaluation belongs to the node whose handler is executing. A handler attached to the overridden node sees that node's workspace; an outer handler sees the enclosing workspace. State transition evaluation occurs in the containing state-machine context after the state boundary exits, with the completed state's outputs available. A whole-machine override therefore remains active across transitions, while an individual-state override does not.

Implement a common enter/execute/leave facility used by every executor entry path, including recipe roots and direct `ExecuteOp`/`ExecuteSequence` calls. A wrapper only around `ExecuteNode` would miss roots. Entry must surround retries and node-local catch handling, with timeout accounting covering target resolution.

Special cases needing explicit plumbing:

- `runState` creates a state context and resolves vars before dispatching its body. Move workspace entry before those vars; consume the declaration so the body does not create a second workspace.
- `ExecuteChildGroup` creates a render context and then synthesizes an op. Both must use the same resolved workspace and boundary ID.
- Include wrapper sequences and included roots retain their own declarations. If both explicitly select a workspace, those are two deliberate boundaries; an inherited declaration is not entered twice.
- Root recipe vars currently resolve before root dispatch. For a root override, enter before root vars, while resolving the selector from submitted inputs and incoming context.
- Validation executors/decorators and timeout/recovery wrappers must preserve the workspace frame and internal task handling.

Use lexical state references rather than mutating a global current-cell variable and trying to restore it with a deferred function. Replay reconstructs boundaries and accepted snapshots; a worker process does not need to survive between tasks.

## Explicit child jobs

`workspace` itself never starts a child job. If the contextualized node explicitly invokes a child recipe or `child_group`, the launch occurs from the active workspace using the existing child-job lifecycle.

Default the child's target cell and Git base coherently to cellB. Update the current `SingleRecipe.CellName` default from job ownership to active workspace identity, with legacy fallback, and update the child-group default similarly. The parent link still identifies the original cellA job; recipe-source lookup keeps its existing semantics. Explicit child-target parameters retain their existing meaning and must not result in a cellA label paired accidentally with a cellB default checkout.

A child job has an independent workspace lineage. If launched from a synthetic cellB snapshot, pass its base and the matching stored thin-pack reference through the child submission path; a synthetic commit hash alone may not exist in the upstream repository. Audit this explicitly because the current launch code separates Git defaults from the list of submitted artifacts. Never pass cellA's suspended pack. Awaiting the child does not automatically merge its Git result into either parent workspace.

## Persistence, compatibility, and diagnostics

Use ordinary resolver task outputs, activity task inputs/outputs, and existing artifact storage. No new JobDB table is needed. Include workspace descriptors in replay/restart state and typed task payload schemas where required by the host.

Recipes without `workspace` must preserve current task ordering, invocation IDs, wire defaults, and snapshots. Do not insert resolver tasks or nonempty scope fields for legacy recipes. Add regression coverage against existing replay histories.

An old worker may silently ignore an unknown YAML field. Deploy workspace-capable compilers and task workers before admitting recipes that use it; mixed fleets need an explicit required-runtime-feature check/routing mechanism. If the host cannot enforce that requirement, keep admission of this syntax disabled until the fleet is upgraded. Schema validation alone is not sufficient protection against old workers ignoring the field.

For each scoped task, log owner cell, workspace cell, scope ID, node path, repository, requested ref, initial commit, and current snapshot. Include workspace provenance with persisted diffs so a cellB diff cannot be displayed as cellA work. Existing job listings remain grouped by the owner cell. Report failures at the node with the target and resolution/restore/persist stage.

## Implementation sequence

1. Add `WorkspaceSpec` to common metadata, schema generation, visitors/copies, recipe hashing, YAML round trips, and validation. Keep runtime admission gated until execution support lands.
2. Extract portable cell resolution and add the job's resolution-context snapshot. Implement the durable resolver task and validation stub.
3. Introduce workspace state in `contextual`/`template`, stable boundary identities, and uniform entry handling across roots, states, includes, and child groups. Separate owner identity from active workspace identity.
4. Scope commit updates, thin-pack forwarding, patches, and implicit result artifacts. Add pinned-base restore to `gitstate`; retain worker checkout/persist machinery.
5. Update Git dependencies, protected environment fields, child defaults/submission snapshot transfer, diagnostics, restart paths, and host payload schemas.
6. Run the tests below, document the syntax in maintained recipe references, upgrade workers, and enable admission.

## Acceptance tests

Use local Git repositories with distinctive content and incompatible histories. The core integration test writes in A, enters a B sequence with two tasks, resumes A, and proves all of: B sees B's files, B's second task sees B's first snapshot, A still sees A's prior changes, and neither repository's source branch was advanced.

Additional required coverage:

- Root op/sequence/state/child-group, nested nodes, individual states, include wrappers, and shared definitions; declarations survive schema validation, normalization, hashing, and serialization.
- Sibling B scopes are independent; B → C → B restores the enclosing B snapshot; explicitly selecting the same cell starts fresh.
- A B machine shares state across loops; a B state gets fresh state on re-entry. Node vars/defaults see B; selectors and caller-side composite bindings see the incoming context.
- Worker retry, composite retry, replay, restart, and execution handoff preserve scope identity, target pin, and accepted snapshots. Move B's branch after resolver completion, then resume on another worker.
- No-change and `const` tasks remain pinned without advancing snapshots. Missing pinned commit/pack and mismatched scope/restore references fail clearly.
- Failure/catch/timeout paths resume A; B Git patches never alter A; unrelated job-global patches retain their existing behavior.
- Last-node-is-B and root-override results cannot advertise B's thin pack as A's primary Git state. User artifacts and diagnostic diffs remain accessible with correct provenance.
- User artifacts with internal-looking names cannot replace the active restore pack. Resolver/checkpoint tasks do not consume or change any scope's pack.
- Dynamic cell/ref expressions, invalid values, inaccessible targets, default refs, skipped nodes, and validation without repository access.
- Recipe/extension source stays pinned while working data changes to B. Job environment and parent provenance remain A; workspace environment reports B.
- Explicit child jobs default coherently to B, can restore an inherited synthetic snapshot, and cannot mutate the parent's workspace by returning a Git result.
- Recipes and persisted task histories without the field retain existing behavior; unsupported workers cannot execute a workspace recipe by ignoring the field.

Run focused suites in `pkg/recipe`, `pkg/template`, `pkg/worker/compiler`, `pkg/worker/ops`, `pkg/git/gitstate`, and `pkg/ops/recipe`, plus a real embedded JobDB fixture covering cross-worker reconstruction. Add resolver tests in its extracted package. Implementation includes parsing/schema, scope/patch, replay, and real Git/job-runtime integration tests.

## Confirmed requirements and remaining design choices

The user confirmed **a fresh workspace per explicit node invocation, shared by its descendants**, and **support for literal and runtime-templated cell selectors**. Optional ref selection and ref templates are proposed alongside that API. Job-wide shared cellB state is outside this design; keying snapshots by cell name would violate the confirmed isolation requirement.

Other proposed choices are `workspace: {cell, ref}` as the syntax, pinning at first entry, and retaining owning-job identity while exposing active workspace identity separately. These can be adjusted without changing the central requirement: each workspace has its own durable Git state and automatic artifact forwarding.
