# Run a recipe node in another cell

Use `workspace` to run an operation or a group of operations against another cell's repository. The job keeps its original identity, and execution resumes in the enclosing workspace when the node exits.

```yaml
id: inspect-dependency
version: "1.0.0"
sequence:
  - id: inspect
    workspace:
      cell: cellB
      ref: main
    op: command_execution
    inputs:
      working_directory: "{{ context.environment.op.worktree_path }}"
      run: "git status --short; cat README.md"
  - id: continue
    op: command_execution
    inputs:
      working_directory: "{{ context.environment.op.worktree_path }}"
      run: "echo Back in the job's original workspace"
outputs:
  report: "{{ sequence.inspect.outputs.stdout }}"
```

The field accepts an object with required `cell` and optional `ref`. Both accept nonempty literal strings or runtime templates. It is available on recipe roots, operations, sequences, state machines, individual states, child groups, and include call sites. Put it on a shared definition or a containing sequence; `shared` reference overrides are not supported.

## Share changes among descendants

Every explicit declaration creates a fresh logical workspace. Descendants without a declaration share its snapshots. Two sibling nodes selecting the same cell get separate workspaces. Group work in a sequence to retain changes between operations:

```yaml
sequence:
  - id: analyze
    workspace: {cell: cellB}
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
```

Selecting the current cell explicitly also creates a fresh workspace. Nesting cellC inside cellB suspends B's state and resumes it afterward. A state-machine declaration shares state across its states and loops; a declaration on an individual state starts fresh on each state entry. Retries and replay within an existing invocation reuse its workspace.

Workspaces are logical, durable state. Every task can run in a different temporary checkout on a different worker. Changes are carried by the existing Git snapshots and thin packs. `const: true` retains its existing meaning: task changes do not advance the snapshot.

## Select a cell dynamically

Selectors are evaluated in the incoming context before entering the workspace. They can use visible inputs, earlier outputs, inherited vars, and transition data. Node-local vars and op defaults resolve after entry and see the selected workspace. Composite input bindings retain caller-side evaluation.

```yaml
id: dynamic-inspection
version: "1.0.0"
input_schema:
  target:
    type: string
    default_value: cellB
inputs:
  target: "{{ inputs.target }}"
sequence:
  - id: inspect
    workspace:
      cell: "{{ inputs.target }}"
      ref: main
    op: command_execution
    inputs:
      working_directory: "{{ context.environment.op.worktree_path }}"
      run: "git log -1 --oneline"
outputs:
  summary: "{{ sequence.inspect.outputs.stdout }}"
```

Cell names use the submitting project's captured naming configuration. `root` and explicit repository locations are also supported. API clients using `recipejob.ResolveTarget` and `BuildStartJob` receive the portable resolution context automatically; clients constructing `StartJob` directly can supply `context.cell_resolution` or use explicit repository locations. A worker never reads its own current directory's project configuration to interpret a selector.

The default ref follows target resolution: configured self/root refs when applicable, otherwise `main`. An override resolves the target ref to a full commit on first entry and records it in task history. Later tasks, retries, and replay reuse that base even if the branch moves. Local paths require access from the worker; use portable Git repository locations for remote workers.

## Context and results

| Field | Meaning inside a cellB workspace owned by a cellA job |
| --- | --- |
| `context.workflow.cell` | cellA, the owning job |
| `context.workspace.cell` | cellB, the working data |
| `context.workspace.scope_id` | Stable identity for this workspace invocation |
| `context.git.*` | cellB's repository, base, author, and current snapshot |
| `context.environment.*` | Current task's worktree, inbox, and outbox paths |
| Recipe and extension sources | Original recipe source, with existing source-resolution rules |

`context.workspace.cell` is also available outside overrides, where it defaults to the owning cell. Commands receive `C2J_WORKSPACE_CELL_NAME` and, for overridden workspaces, `C2J_WORKSPACE_SCOPE_ID`. Existing `C2J_CURRENT_*` variables continue describing job provenance.

Normal node outputs and artifacts remain available through `sequence.<id>` or `states.<name>`. Returning to the parent never adopts the selected workspace's commit. The implicit job-result snapshot belongs to the submitted root workspace, even if the last node—or recipe root—uses another cell. Foreign snapshots and diffs remain in task history with workspace provenance. Internal snapshot filenames (`__git_state_thin_pack__`, `diff_from_parent.diff`, and `diff_from_base.diff`) are reserved in managed-workspace outboxes.

A workspace selection does not change the job's tenant, ownership, execution image, or node execution requirements. It does not reload the recipe from the selected cell. Use `execution_needs` when work needs different execution resources.

Explicit child recipe operations and child groups default to the active workspace's cell and Git data. They receive the matching snapshot when inheriting unmerged changes, while maintaining an independent child lineage. Awaiting their result does not adopt child changes. An explicit child cell override must have a matching Git repository; changing the label alone cannot retarget inherited working data.

## Persistence and deployment

“Ephemeral” means there is no automatic merge or push to the target cell. Snapshotting still preserves work for retries and replay. Explicit Git publishing operations remain subject to existing runtime credentials and external controls; this feature adds no read-only restriction or approval flow.

Repository access failures fail the node and can use normal catch handling. There is no fallback to the original workspace. Git context patches stay inside their workspace boundary.

Recipe validation and isolated recipe-test mocks do not fetch workspace repositories. Use an embedded or deployed runtime for tests that need real checkout, snapshot, and restore behavior.

Upgrade recipe compilers and task workers together before submitting recipes that use `workspace`. Hosts registering task workers individually must register `compiler.NewWorkspaceResolutionTaskWorker()` and use the updated c2j JobDB schema. Older compilers can ignore unknown YAML fields; mixed old/new worker fleets must not admit these recipes until upgraded. Recipes without the field retain their existing task history and snapshot behavior.

Brokered child submissions preserve compiled include metadata, including workspace-bearing includes. Update the submitting CLI and the parent broker worker together when using this support. Authored recipes still cannot supply `__c2j_internal` metadata.

Artifact dependencies have a stable order for replay. The standard runtime can also recover successful tasks recorded by older compilers with a different dependency order, after verifying the original input hash and restore-pack contents. It reads the recorded result without rerunning the task. Other input changes remain determinism errors. Hosts constructing recipe job workers directly can provide `RecipeJobWorkerOptions.TaskHistory`; `NewRecipeWorkerWithOptions` obtains it from a compatible workflow controller automatically.
