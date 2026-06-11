# C2J Git Selector Source Cache Implementation Spec

Status: draft implementation recommendation

Source requirement: `REQUIREMENTS_C2J_EXTENSION_OP_SELECTOR_CACHE.md`

Note: the source requirement file was not present in this workspace when this
spec was written. This proposal is based on the current selector implementation
in `pkg/ops/extensions/selectors.go` and the similar git selector flow in
`pkg/worker/compiler/root_source.go`.

## Recommendation

Build a small reusable git selector source cache, then wire extension op
selectors to it first.

The cache should live below the git layer, for example:

```text
pkg/git/selectorcache
```

The extension package should not own the cache. Extension ops, extension CEL
function packages, and recipe source selectors all need the same primitive:

- resolve a repository ref to an exact commit;
- materialize that exact commit into a local source directory;
- reuse the commit directory across calls and c2j processes.

The first implementation can update only extension selector resolution, but the
API and tests should be independent of extension ops so recipe selectors can use
the same package later without redesign.

## Current State

Extension git selectors are resolved in:

- `pkg/ops/extensions/selectors.go`
- `pkg/worker/compiler/extension_helpers.go`
- `pkg/worker/compiler/within_recipe_resolution.go`
- `pkg/template/extensionfuncs/provider.go`

The current `materializeGitSelector` flow is:

1. clone the selector repository into a temp directory;
2. checkout the requested ref;
3. read the resulting commit;
4. check whether `~/.c2/cache/ops/git/<key>` already exists;
5. return the cached checkout if present, otherwise move the temp checkout into
   the cache.

That cache does not avoid the expensive work. Every selector resolution still
performs a fresh clone and checkout just to rediscover a commit that may already
have a cached source tree.

Recipe git selectors have a related issue in `pkg/worker/compiler/root_source.go`:
they clone and checkout for resolution/load paths as well. Recipe selector reuse
can be implemented later, but the cache primitive should be built for both uses
from the start.

## Design Goals

- Avoid fresh checkout work when a repository commit already has a local source
  directory.
- Reuse cached source directories both within one c2j process and across
  separate c2j invocations.
- Use shallow materialization: fetch only the commit/ref needed for the source
  directory.
- Make a final `<repo-key>/<commit>` directory mean "complete source tree" by
  construction.
- Keep mutable ref behavior correct: branches and tags must still be resolved
  against the remote before deciding which commit directory to use.
- Keep the cache package independent of extension op and recipe selector syntax.

## Non-Goals

- Do not add a new c2j CLI command for cache management in v1.
- Do not add eviction policy in v1.
- Do not cache op outputs.
- Do not implement recipe selector cache wiring in the first change unless it
  remains small. The package should support it, but extension selectors can be
  the first consumer.
- Do not introduce a broad git repository abstraction refactor just for this
  cache.

## Public Shape

The reusable package should operate on normalized repository URLs and refs, not
on c2j selector strings.

Suggested API:

```go
package selectorcache

type Cache struct {
    Root string
}

type ResolveRequest struct {
    RepositoryURL string
    Ref           string
}

type ResolveResult struct {
    RepositoryURL string
    RepoKey       string
    Ref           string
    Commit        string
    SourceDir     string
}

func (c *Cache) Resolve(ctx context.Context, req ResolveRequest) (ResolveResult, error)
```

`pkg/ops/extensions` remains responsible for parsing op selectors and joining
the op path onto `SourceDir`.

`pkg/worker/compiler` remains responsible for parsing recipe selectors and
joining the recipe path onto `SourceDir` when recipe selectors are wired in.

## Cache Root

Use a c2j-owned cache root:

```text
~/.c2j/cache/git-selectors/v1/
```

Recommended layout:

```text
~/.c2j/cache/git-selectors/v1/
  sources/
    <repo-key>/
      <commit>/
        .c2j-source-cache.json
        ...
      .tmp-<random>/
```

`<repo-key>` should be a stable hash of the normalized repository URL. Use a
full SHA-256 hex digest or a long enough prefix to make collision risk
negligible. Do not include an op path or recipe path in the repo key. Multiple
paths inside the same repository commit should share one source directory.

`<commit>` must be the full commit hash.

Add a test hook for the root, either via `Cache{Root: ...}` or an internal
constructor used by tests. A user-facing env var is not needed for the core fix.

## Completion Invariant

Do not build directly into `sources/<repo-key>/<commit>`.

Always materialize into a random temporary directory first:

```text
sources/<repo-key>/.tmp-<random>
```

After the shallow checkout is complete:

1. write `.c2j-source-cache.json` into the temp directory;
2. attempt to move the temp directory to `sources/<repo-key>/<commit>`;
3. if the destination already exists, discard the temp directory and use the
   existing destination.

This makes the final commit directory the completion marker. If a process dies
mid-checkout, it leaves only a random temp directory. Any directory named by a
commit hash is known to have been fully materialized by a previous process.

Do not make normal reads depend on expensive validation such as `git status`.
For v1, existence of the final commit directory is the cache hit. The metadata
file is for debugging and future migration, not for every-hit validation.

Users can recover from manual corruption by deleting the cache directory.

## Shallow Materialization

Materialize only the source tree for the commit needed.

Preferred cold path:

```text
git init <tmp>
git -C <tmp> remote add origin <repository-url>
git -C <tmp> fetch --depth=1 origin <ref-or-commit>
git -C <tmp> checkout --detach FETCH_HEAD
git -C <tmp> rev-parse HEAD
```

If the requested ref was mutable, verify that the checked-out commit is the
commit resolved for that ref. If it differs, fail and retry resolution rather
than caching under the wrong commit.

Do not perform full repository clones in the normal path. For a pinned commit,
try to fetch the commit directly with `--depth=1`. Some git servers may reject
fetch-by-SHA for commits that are not advertised by a ref. In v1, return a clear
error for that cold-cache case rather than silently falling back to a full clone.
The common replay path remains fast because once the commit directory exists, no
network operation is needed.

Optional improvement if compatibility is required later: allow a controlled
fallback mode that fetches a named ref supplied alongside the commit. Do not make
full clone the default behavior.

## Mutable Ref Resolution

Mutable refs such as `@main`, `@HEAD`, and tags cannot be trusted from disk
forever. Resolve them before choosing a commit directory.

Recommended resolution order:

1. If `Ref` is already a full commit hash, use it directly.
2. Otherwise run a cheap remote ref query, for example `git ls-remote`.
3. Normalize the answer to a commit hash.
4. If `sources/<repo-key>/<commit>` exists, return it without checkout.
5. Otherwise shallow-fetch and materialize that commit into a temp directory.

For branch names, query branch refs explicitly where possible:

```text
git ls-remote <repository-url> refs/heads/<branch>
```

For `HEAD`:

```text
git ls-remote <repository-url> HEAD
```

For tags, prefer the peeled tag commit when available:

```text
git ls-remote <repository-url> refs/tags/<tag> refs/tags/<tag>^{}
```

If `ls-remote` cannot resolve an ambiguous ref, fall back to a shallow fetch
into a temp directory and read `rev-parse HEAD`.

## Worktrees

Worktrees are worth considering for a future object-sharing optimization, but
they should not be the cache API.

The cache API should return a standalone source directory at:

```text
sources/<repo-key>/<commit>
```

Final cache entries should not depend on another repository's worktree metadata
for correctness. That keeps deletion simple and preserves the "directory at a
commit hash means complete" invariant.

If object duplication becomes a problem later, `pkg/git/selectorcache` can add
an internal object store per repo key and create temporary worktrees or
reference clones from it. Callers should not know or care. For v1, independent
shallow source directories are simpler and match the core requirement.

## In-Process Cache

Add process-local coalescing for immutable source dirs:

- key: normalized repository URL plus commit;
- value: `SourceDir`.

Use a small per-key mutex map or `singleflight.Group` if adding the dependency
is acceptable. The goal is to prevent concurrent goroutines in one c2j process
from shallow-fetching the same missing commit at the same time.

Do not permanently memoize mutable refs such as `main` to a commit in a
long-running process. The correctness boundary is the resolved commit, not the
branch name.

## Cross-Process Concurrency

The final commit directory path should be lock-free.

Use random temp directories and move-if-absent semantics:

- if the final commit directory already exists, return it;
- otherwise build `sources/<repo-key>/.tmp-<random>`;
- after checkout completes, attempt to rename it to
  `sources/<repo-key>/<commit>`;
- if rename fails because another process won, remove the temp directory and
  return the final commit directory.

The implementation should never replace an existing final commit directory.

This avoids needing to decide whether another process's final directory is
"valid". A final commit directory exists only after a completed move.

## Cache Mutability

Treat final source directories as immutable cache entries.

That matters because extension execution currently runs with:

```go
WorkspaceRoot: resolved.ProjectRoot
WorkingDir:    resolved.OpDir
```

For v1, document that extension packages must write to c2j-provided worktree,
workdir, inbox, or outbox paths, not into the selector source directory.

If practical in the first implementation, make final source directories
read-only after the move. If that breaks existing extension packages, leave
permissions writable for compatibility and add read-only enforcement as a
follow-up. Do not add per-hit validation just to compensate for writable cache
entries.

## Extension Selector Wiring

Change `pkg/ops/extensions/selectors.go` so `resolveGitSelectorPath` uses the
new package:

```go
result, err := selectorcache.Default().Resolve(ctx, selectorcache.ResolveRequest{
    RepositoryURL: parsed.RepositoryURL,
    Ref:           parsed.Ref,
})
```

Then return:

```go
&ResolvedSelectorPath{
    Selector:         selector,
    ResolvedSelector: parsed.WithRef(result.Commit),
    ResolvedCommit:   result.Commit,
    ProjectRoot:      result.SourceDir,
    Dir:              filepath.Join(result.SourceDir, parsed.OpPath),
}
```

This automatically covers extension CEL function packages because they already
call `extops.ResolvePath`.

## Recipe Selector Reuse

Do not bake extension-op assumptions into the cache package.

The later recipe-source change should be able to replace the clone/checkout
flow in `pkg/worker/compiler/root_source.go` with the same cache:

```go
result, err := selectorCache.Resolve(ctx, selectorcache.ResolveRequest{
    RepositoryURL: parsed.RepositoryURL,
    Ref:           parsed.Ref,
})
recipePath := filepath.Join(result.SourceDir, parsed.RecipePath)
```

The recipe resolver should still own recipe parsing, recipe path validation, and
the `RecipeSourceResolution` result shape.

## Code Changes

Primary changes:

- Add `pkg/git/selectorcache`.
- Implement root resolution, repo key hashing, mutable ref resolution,
  shallow materialization, random temp directories, and move-if-absent behavior
  in that package.
- Change `pkg/ops/extensions/selectors.go` to call the cache package.
- Remove or replace extension-only cache helpers such as `extensionCacheRoot`,
  `copyDirTree`, and `copyFile`.

Keep these extension APIs stable:

- `Resolve(ctx, selector, opts)`
- `ResolvePath(ctx, selector, opts)`
- `ResolvedSelectorPath`
- `ResolvedOp`

## Tests

Add package-level tests for `pkg/git/selectorcache` that do not depend on
extension ops:

- pinned commit cache hit returns an existing final commit directory without
  running checkout again;
- mutable branch ref resolves to a commit and reuses the existing commit
  directory on the second call;
- two different paths in the same repo commit can share one `SourceDir`;
- interrupted materialization leaves only `.tmp-*` and is ignored on the next
  call;
- concurrent same-repo same-commit resolution produces one final commit
  directory and no partial final directories;
- cold-cache materialization uses shallow history;
- fetch-by-SHA failure returns a clear error.

Add focused extension tests:

- git extension op selectors use `selectorcache` and preserve
  `ResolvedSelector`;
- extension function packages reuse the same cache through `ResolvePath`;
- same-repo local selectors still pin `./path` to
  `git+<repo>//path@<commit>`.

Existing integration coverage to keep green:

- `pkg/worker/compiler/root_source_test.go` selector pinning tests;
- `pkg/worker/compiler/extension_op_defaults_test.go`;
- `pkg/template/extensionfuncs/provider_test.go`;
- `pkg/ops/extensions/execution_op_test.go`.

## Rollout

Ship this as an internal behavior change for extension selectors first:

- existing selector strings remain valid;
- resolved selectors still pin to exact commits;
- users can recover from cache corruption by deleting
  `~/.c2j/cache/git-selectors`;
- no recipe YAML changes are required.

The visible behavior should be faster repeated selector resolution and fewer
repeated git checkout operations.

After extension selectors are stable on the shared cache, wire recipe source
selectors into the same package.

## Future Work

Possible follow-ups after v1:

- recipe selector cache wiring;
- cache eviction and `c2j cache clean`;
- configurable cache root via env/config;
- read-only source enforcement or per-invocation execution copies;
- optional per-repo object store/worktree optimization behind the same API;
- structured debug logs or counters for selector-cache hit/miss/fetch/rebuild
  events.
