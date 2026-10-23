# Create and publish an empty workspace

Set `initial: empty` on a workspace to start a new Git history. `cell` selects
the destination repository; `ref` selects the branch (defaulting through normal
cell resolution, usually to `main`). The destination repository need not exist.

```yaml
id: create-cell
version: "1"
input_schema:
  prompt: {type: string}
  repository: {type: string, required: true}
  purpose: {type: string, required: true}
inputs:
  repository: "${{ inputs.repository }}"
  purpose: "${{ inputs.purpose }}"
sequence:
  - id: create
    workspace:
      cell: "${{ inputs.repository }}"
      ref: main
      initial: empty
    sequence:
      - id: write
        op: command_execution
        inputs:
          working_directory: "${{ context.environment.op.worktree_path }}"
          env:
            PURPOSE: "${{ inputs.purpose }}"
          run: 'printf "%s\n" "$PURPOSE" > PURPOSE.md'
      - id: publish
        op: squashrebasemerge
        inputs:
          commit_message: "Create cell with approved purpose"
    outputs:
      cell: "${{ context.workspace.cell }}"
      revision: "${{ sequence.publish.outputs.merged_hash }}"
outputs:
  cell: "${{ sequence.create.outputs.cell }}"
  revision: "${{ sequence.create.outputs.revision }}"
```

Run this recipe from an existing recipe source or an embedded local file. Keep
`publish` at your recipe's approved publication boundary. For example:

```sh
c2j submit "Create the approved cell" --advanced-recipe-file ./create.yaml \
  --inputs-json '{"repository":"https://git.example/owner/new.git","purpose":"Approved purpose"}'
```

- Initialization is local only: it does not fetch, push, or create a remote
  repository. Ordinary workspaces never fall back to empty after a fetch error.
- Read-only tasks leave history unborn. A writing task produces a real root
  commit. Native snapshots preserve unpublished history across tasks, workers,
  resumption, and inherited child workspaces. `const: true` still discards changes.
- `squashrebasemerge` squashes all unpublished commits into one root commit and
  pushes only if the destination branch is absent. An existing branch, including
  one created concurrently, causes failure. After publication, subsequent tasks
  use the published commit as their normal base.
- Gitvend can create an authorized missing repository during this ordinary push.
  Other Git servers must already provide the repository. An interrupted push
  discovery may leave an empty remote; retrying can still publish its first branch.
- Inside the workspace, `context.workspace.initial` is `empty` and
  `context.workspace.initial_hash` is absent. `context.git.hash` becomes the real
  persisted HEAD after a writing task. Task-input references
  `context.git.execution.head_hash` and `context.git.execution.base_hash` return
  empty strings when HEAD or the published base does not yet exist. They never
  return a fabricated commit. After publication the base is a real hash;
  `workspace.initial` continues to describe the workspace's original state.
- Exiting the workspace preserves the enclosing cell's Git state. A child can
  inherit this workspace without redeclaring it; another explicit `workspace`
  declaration creates a fresh independent workspace.

Across all workspaces, c2j fills missing Git identity fields with
`colony2.com <col2bot@colony2.com>`. Configured identities and explicit overrides
are honored; preserved original authorship remains distinct from the committer.
Defaults are repository-local, never written to global Git configuration.

`squashrebasemerge` persists the prepared root commit before its internal
publication task runs. If the push succeeds but its response or task outcome is
lost, recovery retries that exact commit. A destination already containing it
counts as success, including when another writer has added descendants. An
unrelated branch is never overwritten. See the [publication recovery
details](BUG-C2J-PUSH-OUTCOME-RETRY.md).

Separate `squash` and `merge` recipe tasks remain available when explicit
preparation and publication are useful. See [separate Git ops](GUIDE-Git-Ops.md).
