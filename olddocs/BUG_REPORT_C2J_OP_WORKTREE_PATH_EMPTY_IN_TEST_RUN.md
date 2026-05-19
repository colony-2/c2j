# Bug Report: `context.environment.op.worktree_path` resolves empty in `c2j test run`

## Summary

`context.environment.op.worktree_path` can resolve to an empty string during `c2j test run`, causing ops with required path inputs to fail validation before execution.

This was observed in the `new-ticket.yaml` merge path where `squashrebasemerge.repo_path` is set from `{{ context.environment.op.worktree_path }}`.

## Environment

- Date detected: 2026-05-15 at 01:11:23 UTC during the full recipe suite
- Reproduced: 2026-05-15 at 04:12:17 UTC with a single selected test case
- c2j: `c2j version dev (built unknown)`, installed from `github.com/colony-2/c2j/cmd/c2j@main`
- Recipe: `new-ticket.yaml`
- Test case: `ts-034-merge-success-completes-job`

## Reproduction

`new-ticket.yaml` currently passes the repo path to `squashrebasemerge` like this:

```yaml
merge:
  op: squashrebasemerge
  inputs:
    repo_path: "{{ context.environment.op.worktree_path }}"
```

Run:

```bash
PATH=/tmp/c2j-main-bin:$PATH \
  /tmp/c2j-main-bin/c2j test run \
  --recipe-file /src/new-ticket.yaml \
  --file /src/recipe-tests/new-ticket.scenario.md \
  --case ts-034-merge-success-completes-job \
  --parallelism 1 \
  --artifact-mode inline \
  --out-dir /tmp/c2j-op-worktree-path-empty-repro \
  --evaluation-mode enforce
```

Actual output:

```text
2026/05/15 04:12:17 INFO executing op op=squashrebasemerge
2026/05/15 04:12:17 ERROR failed to execute op op=squashrebasemerge err="op input validation failed: validate op input: Key: 'SquashRebaseMergeInput.RepoPath' Error:Field validation for 'RepoPath' failed on the 'required' tag"
ts-034-merge-success-completes-job failed (1647ms)
Error: 1 case(s) failed
```

The validation failure implies `{{ context.environment.op.worktree_path }}` resolved to an empty string before `squashrebasemerge` executed.

## Expected Behavior

`context.environment.op.worktree_path` should never resolve to an empty string during recipe execution or recipe test execution.

When no sandbox is configured, the typical/default behavior should be:

```text
context.environment.op.worktree_path == context.environment.host.worktree_path
```

That equality is not a global guarantee for every possible op implementation. A specific op or sandbox configuration may intentionally remap paths. However, the op-visible worktree path should still be populated with the path that the op process or op implementation is expected to use.

## Actual Behavior

In `c2j test run`, the op-visible worktree path appears to be missing from the template context for this built-in op path. The rendered `repo_path` becomes empty, and `squashrebasemerge` fails input validation with a required-field error.

## Impact

- Blocks TS-034 in the recipe test suite.
- Makes the implemented op-visible path contract unreliable for recipes that use built-in git integration ops.
- Encourages recipe authors to work around the issue with `context.environment.host.worktree_path`, even though `op.worktree_path` should be valid in the no-sandbox case and is the preferred default path for op-visible inputs.

## Suggested Fix

Populate `context.environment.op.worktree_path` for all recipe execution paths, including `c2j test run` and built-in ops.

For no-sandbox execution, initialize `context.environment.op.*` from `context.environment.host.*` unless an op or sandbox configuration explicitly supplies a different op-visible path mapping.

Consider adding a c2j regression test that renders all of these fields in `c2j test run` with no sandbox:

```text
context.environment.op.workdir
context.environment.op.worktree_path
context.environment.op.inbox
context.environment.op.outbox
context.environment.host.workdir
context.environment.host.worktree_path
context.environment.host.inbox
context.environment.host.outbox
```

The test should assert none of the `op.*` path fields are empty and that they match the corresponding `host.*` fields in the default no-sandbox case.
