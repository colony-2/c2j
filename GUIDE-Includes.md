# Including recipes from Git

Use an eager include when the target is known while preparing the recipe. Use
`bind: late` when an enabled node must select a reviewed immutable Git recipe
from values available at that point in the caller.

```yaml
sequence:
  - id: approved_build
    include:
      bind: late
      recipe: '${{ inputs.approved_recipe }}'
    inputs:
      message: '${{ inputs.message }}'
    expect_outputs:
      type: object
      required: [result]
      properties:
        result: {type: string}
```

For example, `inputs.approved_recipe` can contain:

```text
git+https://github.com/colony-2/c2j.git//pkg/worker/test-fixtures/recipes/basic-test.yaml@d031eb49c6777555cca1f58449b8451b1fc77c37
```

Ordinary interpolation also works:

```yaml
include:
  bind: late
  recipe: 'git+https://github.com/example/project.git//flows/build.yaml@{{ inputs.commit }}'
```

Declare the caller's submitted inputs in its `input_schema` as usual. Foreign
includes also need an explicit callsite `perms.jobdb` policy; an empty grant is
`perms: {jobdb: {}}`. The [permissions guide](GUIDE-Node-Permissions.md) explains
secret input authorization, delegation and ceilings.

Only the literal binding `late` is supported. Omitting `bind` preserves eager
loading and the existing string/object forms. Explicit `eager`, empty, null,
non-string, templated and unknown bindings fail. A late call needs a nonempty
string `recipe`; its evaluated value must be a remote Git selector with a full
40-character hexadecimal commit hash. Uppercase hashes are accepted and stored
in lowercase. Floating refs, abbreviated or non-hex hashes, local recipe paths,
local Git repositories, bare recipe names, escaping paths, `cell://` targets and
`if_missing` fail. A sequence call requires its own explicit `id`. A direct state
call uses its state key. Selected recipe IDs and versions do not identify the
call.

## Validation and entry

Authored validation checks binding syntax, metadata, permissions, conditions,
input/variable expressions and self-contained `expect_outputs` contracts. It
does not fetch the deferred target or validate against its unknown input schema.
An output contract supplies validation placeholders for subsequent caller
expressions; omitting it does not invent an output schema.

Only an enabled entry evaluates the selector. Selection uses the current caller
context, before callsite/callee variables, workspaces or callee policy are entered.
The whole selector may be a template expression. A skipped call creates no late
selection or capture tasks. Direct state late entry follows this same rule;
ordinary cell-hook preparation retains its existing behavior.

Selection is evaluated inside the native task input builder, after history
lookup. A stable command contains the authored callsite and a history-local
invocation identity. The runtime first accepts a separate selection record with
normalized repository, path and commit, then loads exactly that selection through
the guarded Git resolver. Selector evaluation carries the input builder's
cancellation and deadline into imported CEL function processes; a function's
manifest timeout can shorten that deadline. Loading expands eager descendants
and accepts an expanded
definition artifact with its SHA-256 and source/CEL metadata. Artifact bytes,
digest and selection/source consistency are verified before body effects.
Caller input binding after capture verification carries the active include
lifetime into imported functions. Bound inputs and selected defaults must satisfy
the selected schema. Callee variables, outputs and descendant evaluations inherit
the active body scope lifetime too. Retries and catch matching/handling use their
applicable native contexts; leaving a scope preserves the caller's lifetime and
function memoization.
Included roots support sequences and state machines; wrap root operations and
child groups in a sequence with declared outputs.

## Recovery and failure handling

Selection and capture sit outside the include's body retry. A body retry reuses
one accepted capture; a new state visit or enclosing retry receives a distinct
history-local invocation. Identity includes the enclosing invocation path,
workspace scope and occurrence counter, independent of evaluated targets or
selected recipe identity.

Replay bypasses live selector evaluation for accepted selection tasks and reads
accepted captures without refreshing recipe sources. Captured CEL packages
include their source files, executable modes and internal symlinks so rebuilding their provider
also works with empty source caches. Source capture rejects irregular files and
dangling symlinks or symlinks escaping the captured source root. Operations retain their ordinary
package resolution behavior; this feature does not package operation binaries.

Restart keeps or discards selection/capture with the normal retained-history
prefix. A retained capture uses its original artifact keys even under a new job
or goal. A retained selection with a discarded capture reloads its recorded
commit. Discarding selection permits a new evaluation. Missing, unreadable,
corrupt or inconsistent accepted captures fail; the runtime does not fetch a
replacement.

Preparation, schema, body and output-contract failures remain inside applicable
failure handling. Body catches run at the normal retry boundary, without a
second catch at the transparent preparation boundary. Preparation failures do
not consume body retries. One total deadline covers preparation, body attempts
and retry waits. Direct late states keep this boundary when the machine has a
catch: state handlers run before machine handlers, then an unhandled failure may
retry the body using the same capture. Cancellation remains cancellation, and expired scopes cannot
extend their deadlines through catch handling. An enclosing scope with time
remaining can handle a propagated failure normally.

## Sources, authority and workspaces

Permission inheritance follows repository identity, across commits. A parent at
commit A selecting B in the same repository preserves ordinary inherited grants
and authorized input allowances, restricted by authored grants and all enclosing
ceilings. Crossing into another repository requires explicit delegation. Original
root trust remains fixed; a foreign subtree changing commits does not become the
original root. Passing a secret value does not grant independent secret-name
retrieval authority.

A relative include in B resolves from its declaring file's directory at B.
Relative descendants inherit that commit recursively without repeating its hash.
Explicit child Git selectors retain their authored targets. A nested late call
retains its own declaring source and again requires a full commit. CEL imports
retain the repository/commit of each declaring source, including explicit foreign
children; their preparation uses that source's Git profile and ceilings.

Loading a definition at B does not change the managed workspace's cell, scope,
candidate, dirty bytes or source pin. Explicit workspace declarations retain
ordinary caller bindings, retry behavior and return to the enclosing workspace.
Hooks introduced by a captured definition use ordinary hook policy and capture.

## Validate unpublished changes

Build the candidate CLI and embedded executor from this checkout. The following
smoke fixture fetches a public, fixed c2j commit and runs its existing command
fixture. It needs Git, network access to GitHub and the current configured cell;
the embedded cell needs a local registration, with no remote publication.

```sh
go build -o /tmp/c2j-late-include-check ./cmd/c2j
/tmp/c2j-late-include-check version
/tmp/c2j-late-include-check test validate --recipe-file ./pkg/worker/test-fixtures/recipes/late-bound-includes.yaml --file ./pkg/worker/test-fixtures/recipes/late-bound-includes.test.yaml --strict
/tmp/c2j-late-include-check test run --recipe-file ./pkg/worker/test-fixtures/recipes/late-bound-includes.yaml --file ./pkg/worker/test-fixtures/recipes/late-bound-includes.test.yaml --strict
/tmp/c2j-late-include-check cell create c2j --repo github.com/colony-2/c2j --jobdb embed:///
/tmp/c2j-late-include-check submit --advanced-recipe-file ./pkg/worker/test-fixtures/recipes/late-bound-includes.yaml --inputs-file ./pkg/worker/test-fixtures/recipes/late-bound-includes.inputs.json --run --embed
```

Native acceptance goes beyond the smoke fixture:

```sh
go test ./pkg/recipe -run '^TestLateInclude' -count=1
go test ./pkg/worker/compiler -run '^TestLateInclude' -count=1
```

These tests use actual Git transport, native persistent SQLite/artifact storage,
process crashes and the HTTP runtime wrapper over SQLite. The HTTP wrapper is
not PostgreSQL.

Inside a managed task, `c2j submit --from-workspace-now --advanced-recipe-file
./path/to/check.yaml` can submit current unpublished workspace contents through
the broker. `--from-workspace` uses the incoming snapshot and excludes current
edits. Neither option upgrades a worker runtime: validation of unpublished c2j
must build/use this candidate CLI and executor. Submit's supported custom-recipe
flag is `--advanced-recipe-file`; `--recipe-file` is its deprecated alias.
