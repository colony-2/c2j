# Submit related work to one recipe job

Add `grouping` to a recipe submission when several callers should contribute to
one pending receiver. Each contribution remains separate and ordered. The receiver
runs once per generation and returns one batch result to its callers.

```yaml
op: recipe.run_and_get_result
inputs:
  name: merge-batch
  cell_name: api
  git_ref: main
  inputs: {branch: main}
  grouping:
    key: api/main/merge-v1
    content: {pr: 1234, head_sha: 9f2c1e}
    artifacts: []
    max_items: 50
```

Put the same property on individual entries in `recipes.run` or
`recipes.run_and_wait`, or beside `recipe` on static/generated `child_group`
children. Mixed ordinary and grouped submissions work. Await/get operations use
existing handles and do not accept grouping.

`key` is a literal, nonblank UTF-8 string of at most 1024 bytes without NUL. Its
bytes are preserved. Provide a non-null content object (including `{}`), at least
one artifact, or both. Explicit null grouping/content/artifacts is invalid.
Omitted artifacts become `[]`. After rendering, `max_items` must be an integer
from 0 through 1000; omission or zero uses 50. This counts contributions, including
the creating contribution. Each call chooses its own admission limit; the backend
does not save a permanent group-wide cap.

The receiving recipe reads `context.grouping.appends`:

```yaml
sequence:
  - id: inspect
    op: command_execution
    artifacts:
      request.txt: '${{ context.grouping.appends[0].artifacts[0] }}'
    inputs:
      run: 'cat "${{ context.environment.op.inbox }}/request.txt"'
outputs:
  first_pr: '${{ context.grouping.appends[0].content.pr }}'
```

An artifacts-only contribution has no content property. Grouping remains
separate from ordinary creator inputs and their input schema. Creator artifacts
retain their usual names in `context.artifacts`; contribution artifacts stay in
their respective append entries. Append-supplied recipe YAML cannot replace the
creator recipe. Nested scopes inherit the grouping context.

Use concrete c2j stored, external or value references. Submission uploads no new
files and performs no external fetch. Keep aliases and descriptor order; native
stored keys retain their original identity. Repeating one stored key with distinct
aliases is supported. Distinct stored keys with the same original name within a
single contribution are invalid, even with aliases. Equal filenames in different
contributions work. Effective names within each contribution must be unique clean
relative paths, without traversal, backslashes or NUL. Unresolved `attached`
references must first be replaced by concrete references. References grant no
additional access or retention; missing bytes may fail when the receiver reads them.

Jobs share a chain by tenant, immutable cell identity, type `recipe` and literal
key. The first creator chooses each generation's recipe, configuration, workspace,
schema and execution permissions. Joining callers do not replace or compare that
template. Share keys only among compatible callers; version the key when changing
receiver configuration, source or authority. Recipe name and Git ref do not
separate chains.

A full, worker-held, sealed or cancelled tail routes new work to a successor.
Successors wait for the preceding generation to terminate, including failure or
completed cancellation. There is no batching delay or minimum batch size. Avoid
synchronously waiting on another submission to the same chain from its running
receiver, because the successor waits for that receiver to finish.

Grouped result calls default to exact-job success. Grouped wait-only calls default
to exact-job terminal completion. Explicit inherited/per-child wait policies retain
precedence. An explicit goal wait can accept a repair job's result. Ordinary calls
and previously saved policies retain their existing defaults. Durable start steps
save the complete returned identity and effective wait.

Shared jobs have no exclusive parent. Cancelling a caller stops its wait without
withdrawing accepted work or cancelling the receiver. Explicit authorized batch
cancellation affects all callers. A completed start checkpoint reuses its handle
on replay. Acceptance followed by failure before that checkpoint may deliver a
contribution twice, including to a successor. Receivers needing deduplication must
use stable application request IDs and retain deduplication state across batches.

The item limit does not bound total memory or aggregate input bytes. Creator JSON
and the first append share one chapter's application budget; later appends have
individual budgets. Size violations fail; there is no aggregate-byte rollover.
Transport errors can follow acceptance and are returned without an automatic
second contribution. Unsupported grouped methods fail without ordinary submission
or root-client fallback.

The implementation consumes JobDB `v0.0.92-0.20261117232744-d7820fd875fe`,
published at `d7820fd875feed7ecb5fa7d34d8a26e7846af3af`. This includes grouped
history/result inspection, cold-schema HTTP recovery and delegated grouped
submission authorization. HTTP servers must contain the correction too; updating
only the client cannot repair an older server. Failed calls do not use a different
submission route.

Compatible PostgreSQL scheduler/DDL and authentic populated preserving-upgrade
acceptance are also required before claiming backend support. This change does
not deploy or migrate a database. Stop old writers, keep a coherent backup, and
enable compatible readers before grouped producers. Older readers cannot decode
grouped histories; rollback requires a compatible reader or restoring the backup.
