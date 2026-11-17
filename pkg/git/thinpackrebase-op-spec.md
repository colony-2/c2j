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

For ordinary replay, only `target_base_hash` is required from the recipe. The defaults are:

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

## Adopt a confirmed resolved tree (operation metadata version 1.2.0)

Both `rebase` and `thinpackrebase` accept optional `resolution`. Supply all four
confirmed full 40-character object IDs explicitly; replay defaults cannot supply
adoption confirmations. Omission selects ordinary replay. Null, empty or malformed
resolution fails rather than falling back to replay.

```yaml
- id: adopt
  op: rebase
  inputs:
    base_hash: '${{ sequence.confirm.outputs.base_hash }}'
    persist_hash: '${{ sequence.confirm.outputs.persist_hash }}'
    target_base_hash: '${{ sequence.confirm.outputs.target_base_hash }}'
    resolution:
      tree_hash: '${{ sequence.confirm.outputs.tree_hash }}'
```

Adoption is available only on the active managed workspace with native base-setting
support and persistence enabled (`const: true` is rejected). The expected base must equal its restored authoritative base, and the
candidate must equal both its accepted persisted identity and HEAD. The three
commit IDs must name commits directly, and `tree_hash` must name the candidate's
complete tree. Tags are not commit confirmations. The workspace and index must
be clean, with no merge, rebase, cherry-pick, revert or sequencer session.

The base must be an ancestor of both candidate and target; the target must also
respect the workspace floor. Bounded history retrieval proves the pinned
relations without substituting a remote's newer tip. The candidate need not
descend from the target.

Adoption constructs one commit with the exact confirmed tree and the target as
sole parent. It never replays or adds missing target files. It preserves the
candidate's author and full message by default; `preserve_author: false` uses
the configured identity. If tree equals the target tree, it returns target
itself, preserving its metadata and creating no empty commit.

`new_base_hash` and `target_base_hash` equal the pinned target;
`new_persist_hash` names the verified candidate or target. `rebased_from`
records the confirmed old pair. Native worker persistence records this pair
with a replacement bundle and target-relative diff. Since parent equals base,
their shared diff is written once as `diff_from_parent.diff`. Equal-tree
adoption records target as candidate/base and drops the obsolete bundle.

The durable successful task outcome is the acceptance boundary. Local checkout
changes and prepared artifacts do not install an accepted snapshot. A replacement
worker restores the prior accepted pair after construction, persistence or
pre-recording failure; after recorded success it restores the new pair. Completed
offline replay returns recorded outputs without touching Git. Required verification
and fresh review must run on the adopted candidate before publication.

## Structured failures

The public `rebase` and `thinpackrebase` selectors emit native task failures;
successful outputs and Git context updates are unchanged. This contract does
not apply to the composite `squashrebasemerge` selector.

Recipe catches can route on `failure.kind == 'task_error'` and
`failure.code == 'REBASE_CONFLICT'`. `failure.attrs.rebase` contains:

| Field | Meaning |
| --- | --- |
| `version` | `1` |
| `phase` | `prepare`, `fetch`, `ancestry`, `fast_forward`, `replay`, `validate_resolution`, `construct_resolution`, `install_resolution`, `update_ref`, or `adopt_base` |
| `base_hash`, `candidate_hash`, `target_base_hash` | Available hashes, or explicit null when unavailable; values resolved during execution use full hashes. |
| `conflicts` | List of `{path, status}` from Git's NUL-delimited porcelain status, collected before abort. |
| `workspace_restored` | True when the workspace was untouched, or cleanup verified the original candidate and clean index/worktree. |
| `diagnostics` | Map of durable artifact refs, usable as inputs/artifact bindings in later operations. |
| `cleanup_error` | Present if restoration failed. |
| `diagnostic_errors` | Collection/publication errors, if any; these do not replace the original failure. |

`REBASE_CONFLICT` requires confirmed unmerged index state. Other application
errors use `REBASE_FAILED`, preserving an existing more specific native code.
Cancellation, timeout, and system errors keep their native classification and
are never made repairable by text matching. Diagnostic artifacts are also
attached to failed outcomes when available.

Conflict evidence includes Git stderr and combined output, the unmerged index
(`conflict-index.z`), a conflict diff, and the stopped commit's patch under
`_c2j/rebase/`. Diffs live in artifacts, not error attributes. Failed rebase
does not adopt a new base or candidate. Cleanup failure leaves
`workspace_restored: false`; automatic repair must require true.

Catch recovery uses the retained pre-operation candidate, the recorded target
hash, and these durable references on a fresh worker. The rebase session has
been aborted: do not depend on `git rebase --continue` or on a worker-local
directory. Resolve the changes in a subsequent operation and persist a clean
candidate under the old base. Confirm that new candidate and tree, then adopt
them with the same pinned target using `resolution`, and verify again. Match the
originating replay node, supported evidence version and restored state; adoption
failures never qualify for conflict repair. Enclosing catches and `transition.failure` preserve
the originating node and versioned data, including on completed replay.

## Coverage

Tests cover real child merge → refresh → next task; omitted defaults; shallow
history with present/missing targets; pending commits; fast-forward; already
merged local work; divergence and conflicts; dirty work preservation; base
constraints; scoped workspaces; suspension/resumption; and offline replay.
Adoption tests additionally cover exact-tree fidelity, both selectors, stale and
missing confirmations, Git object types, unfinished integration states, scoped
isolation, construction/install failures, failed bundle/diff generation, artifact
upload failure and worker loss before/after durable recording. The runnable
[`resolved-tree` suite](../../testdata/repros/resolved-tree/runtime.test.yaml)
exercises native adoption and fresh restoration using a built CLI.
