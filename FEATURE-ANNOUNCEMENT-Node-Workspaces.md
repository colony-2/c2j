# Run recipe nodes in another cell

Recipes can now use `workspace` to run an op, sequence, state machine, or child group against another cell's repository:

```yaml
- id: inspect_dependency
  workspace:
    cell: "{{ inputs.dependency }}"
  op: command_execution
  inputs:
    working_directory: "{{ context.environment.op.worktree_path }}"
    run: "cat README.md"
```

Each explicit node gets a fresh workspace, shared by its descendants. Existing snapshots preserve changes across tasks, retries, and replay. When the node finishes, execution resumes in the enclosing workspace without merging its changes.

Cell selectors support literals and runtime templates, with an optional `ref`. Job ownership stays with the original cell; `context.workspace.cell` identifies the active workspace.

There is no automatic push or merge to the selected cell.

See the [workspace guide](GUIDE-Node-Workspaces.md) for examples and deployment requirements.
