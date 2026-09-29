<!-- Sources: pkg/recipe/workspace.go; pkg/worker/compiler/workspace.go; pkg/template/workspace.go; GUIDE-Node-Workspaces.md. -->

# Cell Workspaces

Use node metadata `workspace: {cell: cellB, ref: main}` for work against another cell's data in the same job. `cell` is required; `ref` is optional. Both support literals and runtime templates. Apply it to an op, sequence, state machine, individual state, child group, include call site, or recipe root. Put it on a shared definition or containing sequence, not on a `shared` reference.

```yaml
sequence:
  - id: inspect
    workspace:
      cell: "{{ inputs.target }}"
    op: command_execution
    inputs:
      working_directory: "{{ context.environment.op.worktree_path }}"
      run: "cat README.md"
```

The selector uses the incoming scope's visible inputs, prior outputs, inherited vars, and transition data. It cannot use vars declared on the same node. Node-local vars and op defaults see the selected workspace; composite input bindings see the caller. Preserve normal input forwarding into composite scopes, including explicit root `inputs` bindings when needed.

Each explicit declaration creates a fresh workspace, including repeated declarations of the same cell. Descendants without declarations share that workspace's changes through ordinary snapshots. To share changes across several operations, put one declaration on their containing sequence or state machine. A declaration on an individual state starts fresh each time that state is entered; a machine declaration lasts across its states and loops.

The target ref is pinned on first entry and reused for retries and replay. Omitted refs follow cell resolution defaults: configured self/root refs, otherwise `main`. Configured short names use the submitting project's captured naming pattern. Explicit Git repository locations work without that configuration. Local paths require access on the worker.

Inside an override:

- `context.workspace.cell` identifies the active cell; `context.workspace.scope_id` identifies the invocation.
- `context.workflow.cell` remains the owning job's cell.
- `context.git.*` and worktree paths refer to the active workspace.
- Recipe includes and extension code retain their existing recipe source.

Outputs and user artifacts cross the boundary normally. Git commits, thin packs, and Git context patches do not automatically advance the enclosing workspace. Snapshots remain durable; “ephemeral” does not imply `const: true`. The implicit job-result snapshot remains the root workspace's snapshot, even when the final node runs elsewhere.

Explicit child jobs default to the active cell and can inherit its synthetic snapshot; their returned changes are not adopted by the parent. Changing a child's cell label alone does not retarget its Git repository.

Workspace entry/exit never pushes or merges to the selected cell. Existing explicit publishing ops and external access controls keep their normal behavior. No additional approval or read-only mode is implied.

Use isolated recipe tests for templates and mocked flow; use a real embedded/deployed runtime for checkout and snapshot assertions. Upgrade recipe compilers and task workers together before using the field; old workers may ignore unknown YAML fields.
