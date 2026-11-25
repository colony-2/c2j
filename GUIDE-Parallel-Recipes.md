# Run inline recipe branches in parallel

Use `parallel` at the root of a recipe or wherever you would use a `sequence`. Give each branch a unique, nonempty `id`. Branches use ordinary node syntax and can be skipped with `when`, evaluated before launch. Pass values into the collection with `inputs`, as with a nested sequence.

```yaml
id: checks
version: "1"
sequence:
  - id: verify
    parallel:
      mode: wait_all
      branches:
        - id: lint
          op: command_execution
          inputs: {run: "go vet ./..."}
        - id: tests
          op: command_execution
          inputs: {run: "go test ./pkg/recipe"}
    outputs:
      log: '${{ parallel.tests.outputs.stdout }}'
outputs:
  test_log: '${{ sequence.verify.outputs.log }}'
```

Each active branch gets its own parent-linked job. Branches can overlap subject to worker capacity; a running branch cannot read its siblings' results. In the collection's `outputs`, read results as `parallel.<id>.outputs` and artifact references as `parallel.<id>.artifacts`. To use a branch artifact in a later node, declare the reference in the collection's outputs and bind it to that node's `artifacts`.

`wait_all` is the default and only mode. The collection attempts all launches, waits for every started job to reach a terminal state, then fails if any launch was rejected or any branch failed, timed out, or was cancelled. A transient catalog failure is retried by the launch task; only a confirmed rejection is reported as `start_failed`. A handler on the collection can inspect `failure.attrs.parallel` for results and diagnostics keyed by branch ID. Timing out the collection does not cancel its branches.

Branches see the enclosing state's `transition` data and inherit scoped `execution_needs`, just as nodes in a sequence do. A branch can override an inherited requirement with its own `execution_needs`.

All branches use the caller's resolved recipe source and the same Git state. A floating workspace ref is pinned for the collection, not for later parent steps. A branch can read the parent's accepted snapshot, but cannot switch workspaces, publish Git changes with built-in Git ops, or pass its workspace changes back. The workspace is `const` throughout the branch; commands can still have side effects outside the managed Git state. Secret permissions carry over; configuration and secret values are loaded independently in each job.

Upgrade recipe readers and workers before using `parallel`. Remote coordination uses ordinary JobDB runtime ingress; delegated-only ingress is not supported.
