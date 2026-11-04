<!-- Sources: pkg/recipe/workspace.go; pkg/worker/compiler/workspace.go; pkg/template/workspace.go; GUIDE-Node-Workspaces.md. -->

# Cell Workspaces

Use node metadata `workspace: {cell: cellB, ref: main}` for work against another cell's data in the same job. `cell` is required; `ref` is optional. Both support literals and runtime templates. Add literal `initial: empty` to initialize locally without fetching; see [empty workspaces](https://github.com/colony-2/c2j/blob/main/GUIDE-Empty-Workspaces.md). Writing tasks persist self-contained snapshots until `squashrebasemerge` publishes a root squash commit to an absent branch. Apply it to an op, sequence, state machine, individual state, child group, include call site, or recipe root. Put it on a shared definition or containing sequence, not on a `shared` reference.

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

Symbolic refs remain unpinned until the first accepted repository change or explicit Git base adoption. Each task attempt selects a valid hash for the ref; consecutive tasks, internal steps, and retries may use different hashes. Const/no-change tasks do not pin later work. Accepted snapshots and exact commit refs stay fixed until explicit integration. Rebase advances the active snapshot base and HEAD, keeping patch changes separate from incorporated upstream changes. Completed replay uses recorded results. Restart reconstructs the retained prefix: discarded writes do not pin future work, retained writes remain durable. Old execution stores are incompatible with the named-cell cutover. Fresh workspace entries resolve within task workers. Omitted refs follow cell resolution defaults: configured self/root refs, otherwise `main`. Cell names resolve through JobDB. Captured routing configuration and optional `workspace.repo` supply repository assertions; they must agree with the registry. Repository URLs and local paths are not cell names.

Inside an override:

- `context.workspace.cell` identifies the active cell; `context.workspace.scope_id` identifies the invocation.
- `context.workflow.cell` remains the owning job's cell.
- `context.git.*` and worktree paths refer to the active workspace.
- Recipe includes and extension code retain their existing recipe source.

Outputs and user artifacts cross the boundary normally. Git commits, thin packs, and Git context patches do not automatically advance the enclosing workspace. Snapshots remain durable; “ephemeral” does not imply `const: true`. The implicit job-result snapshot remains the root workspace's snapshot, even when the final node runs elsewhere.

Explicit child jobs default to the active cell and can inherit its synthetic snapshot; their returned changes are not adopted by the parent. Child target names resolve through JobDB; use optional `cell_repo` for a repository assertion.

For CLI submissions from a task, choose between its incoming snapshot and live
workspace contents using the flags documented in
[Starting From Workspace State](testing-and-cli.md#starting-from-workspace-state).
Parent-job linkage alone does not make `c2j submit` inherit the workspace snapshot.

Workspace entry/exit never pushes or merges to the selected cell. Existing explicit publishing ops and external access controls keep their normal behavior. No additional approval or read-only mode is implied.

Use isolated recipe tests for templates and mocked flow; use a real embedded/deployed runtime for checkout and snapshot assertions. Upgrade recipe compilers and task workers together before using the field; old workers may ignore unknown YAML fields.

Use separate [`squash`, `rebase`, and `merge` tasks](https://github.com/colony-2/c2j/blob/main/GUIDE-Git-Ops.md) to persist preparation before publication. `merge` is fast-forward only. For an empty workspace, use `squash` then `merge` without `rebase`.

See [named cells](https://github.com/colony-2/c2j/blob/main/GUIDE-Named-Cells.md) for explicit registration, permissions, and the storage cutover.
