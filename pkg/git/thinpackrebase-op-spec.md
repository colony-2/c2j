# Thinpack rebase

`thinpackrebase` refreshes the active workspace to a newer repository base,
replaying any pending local commits on top of it.

```yaml
- id: refresh
  op: thinpackrebase
  inputs:
    target_base_hash: '${{ sequence.child.outputs.outputs.merged_hash }}'
```

## Defaults and execution context

Only `target_base_hash` is required from the recipe. The defaults are:

| Input | Default |
| --- | --- |
| `repo_path` | `{{ context.environment.worktree_path }}` |
| `base_hash` | `{{ context.git.execution.base_hash }}` |
| `persist_hash` | `{{ context.git.execution.head_hash }}` |
| `base_repo` | `{{ context.git.repo }}` |
| `git_author` | `{{ context.git.author }}` (otherwise the cell's author identity) |

The two `context.git.execution.*` expressions are task-time references, usable
in op inputs. They resolve after the workspace is restored, before invoking the
op. `base_hash` is the base beneath pending commits; `head_hash` is the actual
restored HEAD. Both `{{ ... }}` and `${{ ... }}` syntax work. They are not
immediately available hashes for recipe routing; use a completed op's outputs
when a decision needs a hash.

`context.git.resolved_hash` keeps its existing meaning and can intentionally be
empty before work commits to a hash. Reading an execution reference does not
pin an otherwise uncommitted recipe. A successful refresh explicitly commits
the workspace to the selected base and rebased HEAD.

Explicit hashes remain supported. For a separate checkout, supply its
`repo_path`, `base_hash`, and `persist_hash` explicitly; the defaults describe
the managed workspace. A separate checkout's refresh does not update managed
workspace metadata.

## Behavior

- The target must descend from the supplied current base and respect the
  managed workspace's initial base when available. Equal bases are allowed.
- A target commit being present locally does not prove its ancestry is present.
  Fetch target history incrementally when shallow boundaries hide the relation.
  There is no unconditional full-history clone or unshallow operation. Truly
  divergent history fails; unavailable history fails as unverified ancestry.
- With no pending commits, fast-forward to the target. Otherwise, rebase pending
  commits onto it. `preserve_author` defaults to true; a committer identity is
  supplied even when retaining original authors.
- Uncommitted changes are rejected without discarding them. Conflicts abort the
  rebase and retain the original commits.
- On the managed workspace, update the persistence base and carry a replacement
  thin pack containing only remaining pending work. A refresh with no pending
  work needs no thin pack. Subsequent tasks and resumed execution restore this
  completed state, even if the repository ref advances again.
- Completed task replay uses the recorded outcome without fetching or rebasing.
  Whole-job retries are separate attempts and keep their existing policy.

`upstream_remote` optionally selects the fetch remote. `update_refs` optionally
updates a local ref to the resulting HEAD.

Outputs: `target_base_hash`, `new_base_hash`, `new_persist_hash`, `updated_ref`,
`rebased_from`, and `git_context_patch`. The patch is descriptive output; native
workspace persistence uses an explicit runtime update, not arbitrary output
fields named `git_context_patch`.

## Coverage

Tests cover real child merge → refresh → next task; omitted defaults; shallow
history with present/missing targets; pending commits; fast-forward; already
merged local work; divergence and conflicts; dirty work preservation; base
constraints; scoped workspaces; suspension/resumption; and offline replay.
