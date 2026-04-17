# Migration Guide

This repo used to live as a set of top-level folders under the monorepo path:

`github.com/colony-2/colony2/server`

Examples of the old layout:

- `github.com/colony-2/colony2/server/recipe-core/pkg/recipe`
- `github.com/colony-2/colony2/server/recipe-template/pkg/template`
- `github.com/colony-2/colony2/server/recipe-worker/pkg/compiler`
- `github.com/colony-2/colony2/server/c2j/pkg/c2jops`

It is now a single Go module:

`github.com/colony-2/c2j`

This guide is for external users updating imports, build scripts, and any code that embedded the old package layout.

## Summary

The migration has three parts:

1. Change the module prefix from `github.com/colony-2/colony2/server/...` to `github.com/colony-2/c2j/...`.
2. Update package paths to the flatter `pkg/...` layout.
3. Stop importing `c2jops` directly. It is now internal to the `c2j` command.

## High-Level Mapping

The old repo was organized by project folder:

- `core`
- `git`
- `c2jconfig`
- `recipe-core`
- `recipe-input`
- `recipe-template`
- `recipe-worker`
- `recipe-child`
- `ops`
- `c2j`

The new repo is organized around:

- `cmd/c2j`
- `pkg/...`

The common rule is:

- old: `github.com/colony-2/colony2/server/<project>/pkg/<name>`
- new: `github.com/colony-2/c2j/pkg/<name-or-namespace>`

## Import Path Changes

### Core runtime packages

| Old import path | New import path |
| --- | --- |
| `github.com/colony-2/colony2/server/recipe-core/pkg/artifacts` | `github.com/colony-2/c2j/pkg/artifacts` |
| `github.com/colony-2/colony2/server/recipe-core/pkg/cel` | `github.com/colony-2/c2j/pkg/cel` |
| `github.com/colony-2/colony2/server/recipe-core/pkg/contextual` | `github.com/colony-2/c2j/pkg/contextual` |
| `github.com/colony-2/colony2/server/recipe-core/pkg/ops` | `github.com/colony-2/c2j/pkg/ops` |
| `github.com/colony-2/colony2/server/recipe-core/pkg/recipe` | `github.com/colony-2/c2j/pkg/recipe` |
| `github.com/colony-2/colony2/server/recipe-core/pkg/starter` | `github.com/colony-2/c2j/pkg/starter` |
| `github.com/colony-2/colony2/server/recipe-core/pkg/swfutil` | `github.com/colony-2/c2j/pkg/swfutil` |
| `github.com/colony-2/colony2/server/recipe-core/pkg/task` | `github.com/colony-2/c2j/pkg/task` |
| `github.com/colony-2/colony2/server/recipe-core/pkg/workflow` | `github.com/colony-2/c2j/pkg/workflow` |
| `github.com/colony-2/colony2/server/recipe-core/pkg/workflowctl` | `github.com/colony-2/c2j/pkg/workflowctl` |

### Template and input packages

| Old import path | New import path |
| --- | --- |
| `github.com/colony-2/colony2/server/recipe-template/pkg/template` | `github.com/colony-2/c2j/pkg/template` |
| `github.com/colony-2/colony2/server/recipe-template/pkg/colonycel` | `github.com/colony-2/c2j/pkg/template/colonycel` |
| `github.com/colony-2/colony2/server/recipe-template/pkg/funcregistry` | `github.com/colony-2/c2j/pkg/template/funcregistry` |
| `github.com/colony-2/colony2/server/recipe-input/pkg/input` | `github.com/colony-2/c2j/pkg/input` |
| `github.com/colony-2/colony2/server/recipe-input/pkg/openapi` | `github.com/colony-2/c2j/pkg/input/openapi` |

### Worker packages

| Old import path | New import path |
| --- | --- |
| `github.com/colony-2/colony2/server/recipe-worker/pkg/activity` | `github.com/colony-2/c2j/pkg/worker/activity` |
| `github.com/colony-2/colony2/server/recipe-worker/pkg/commandop` | `github.com/colony-2/c2j/pkg/worker/commandop` |
| `github.com/colony-2/colony2/server/recipe-worker/pkg/compiler` | `github.com/colony-2/c2j/pkg/worker/compiler` |
| `github.com/colony-2/colony2/server/recipe-worker/pkg/executor` | `github.com/colony-2/c2j/pkg/worker/executor` |
| `github.com/colony-2/colony2/server/recipe-worker/pkg/export` | `github.com/colony-2/c2j/pkg/worker/export` |
| `github.com/colony-2/colony2/server/recipe-worker/pkg/ops` | `github.com/colony-2/c2j/pkg/worker/ops` |
| `github.com/colony-2/colony2/server/recipe-worker/pkg/workflow` | `github.com/colony-2/c2j/pkg/worker/workflow` |

### Ops packages

| Old import path | New import path |
| --- | --- |
| `github.com/colony-2/colony2/server/ops/pkg/extensions` | `github.com/colony-2/c2j/pkg/ops/extensions` |
| `github.com/colony-2/colony2/server/recipe-child/pkg/recipe` | `github.com/colony-2/c2j/pkg/ops/recipe` |
| `github.com/colony-2/colony2/server/recipe-worker/pkg/sleepop` | `github.com/colony-2/c2j/pkg/ops/sleepop` |

### Git and utility packages

| Old import path | New import path |
| --- | --- |
| `github.com/colony-2/colony2/server/git/pkg/git` | `github.com/colony-2/c2j/pkg/git` |
| `github.com/colony-2/colony2/server/git/pkg/common` | `github.com/colony-2/c2j/pkg/git/common` |
| `github.com/colony-2/colony2/server/git/pkg/export` | `github.com/colony-2/c2j/pkg/git/export` |
| `github.com/colony-2/colony2/server/git/pkg/gitcollector` | `github.com/colony-2/c2j/pkg/git/gitcollector` |
| `github.com/colony-2/colony2/server/git/pkg/gitcommit` | `github.com/colony-2/c2j/pkg/git/gitcommit` |
| `github.com/colony-2/colony2/server/git/pkg/gitshallow` | `github.com/colony-2/c2j/pkg/git/gitshallow` |
| `github.com/colony-2/colony2/server/git/pkg/gitstate` | `github.com/colony-2/c2j/pkg/git/gitstate` |
| `github.com/colony-2/colony2/server/git/pkg/squashrebasemerge` | `github.com/colony-2/c2j/pkg/git/squashrebasemerge` |
| `github.com/colony-2/colony2/server/git/pkg/thinpackrebase` | `github.com/colony-2/c2j/pkg/git/thinpackrebase` |
| `github.com/colony-2/colony2/server/core/pkg/core` | `github.com/colony-2/c2j/pkg/core` |
| `github.com/colony-2/colony2/server/core/pkg/file` | `github.com/colony-2/c2j/pkg/file` |
| `github.com/colony-2/colony2/server/core/pkg/logutil` | `github.com/colony-2/c2j/pkg/logutil` |
| `github.com/colony-2/colony2/server/c2jconfig/pkg/config` | `github.com/colony-2/c2j/pkg/config` |

### Test fixture packages

If you imported test helpers directly, these also moved:

| Old import path | New import path |
| --- | --- |
| `github.com/colony-2/colony2/server/recipe-worker/test-fixtures` | `github.com/colony-2/c2j/pkg/worker/test-fixtures` |
| `github.com/colony-2/colony2/server/recipe-input/test-fixtures` | `github.com/colony-2/c2j/pkg/input/test-fixtures` |
| `github.com/colony-2/colony2/server/recipe-child/test-fixtures` | `github.com/colony-2/c2j/pkg/child/test-fixtures` |

## `c2jops` Is No Longer Public

The old helper:

- `github.com/colony-2/colony2/server/c2j/pkg/c2jops`

was always tied to the `c2j` command’s runtime composition. It is now internal:

- `github.com/colony-2/c2j/cmd/c2j/internal/c2jops`

External code should not import it.

If you were using it to register the standard c2j op set, wire that registration yourself from public packages instead. The equivalent public pieces are:

- `github.com/colony-2/c2j/pkg/ops`
- `github.com/colony-2/c2j/pkg/ops/extensions`
- `github.com/colony-2/c2j/pkg/ops/recipe`
- `github.com/colony-2/c2j/pkg/input`
- `github.com/colony-2/c2j/pkg/git/export`
- `github.com/colony-2/c2j/pkg/worker/export`

Example:

```go
package main

import (
	gitexport "github.com/colony-2/c2j/pkg/git/export"
	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	workerexport "github.com/colony-2/c2j/pkg/worker/export"
)

func registerC2JLikeOps() {
	impls := []ops.RegisterableOp{extensions.GetExecutionOp()}
	impls = append(impls, workerexport.GetAll()...)
	impls = append(impls, input.GetOp(), input.GetAutoFillOp())
	impls = append(impls, recipeops.GetOps()...)
	impls = append(impls, gitexport.GetAll()...)
	ops.Replace(impls...)
}
```

## CLI and Build Changes

The `c2j` command now builds from:

- `github.com/colony-2/c2j/cmd/c2j`

If you were building from the old monorepo path, update your scripts accordingly.

The repo also no longer uses per-project `moon.yml` files. The supported top-level source tasks are now:

- `make build`
- `make test`
- `make fmt`
- `make openapi`

## Suggested Migration Procedure

1. Update your `go.mod` dependency from `github.com/colony-2/colony2/server/...` usage to `github.com/colony-2/c2j`.
2. Rewrite imports using the tables above.
3. Replace any direct `c2jops` imports with explicit public-package registration.
4. Run `go test ./...` in your downstream repo.
5. If you build `c2j` from source, switch your build scripts to `make build` or `go build -buildvcs=false ./cmd/c2j`.

## Notes

- Most package names did not change. In most cases only the import path changed.
- `pkg/ops` is now the shared ops runtime package and also the namespace root for shipped ops such as `pkg/ops/extensions`, `pkg/ops/recipe`, and `pkg/ops/sleepop`.
- `pkg/core` still exists as a real package. Only the former namespace-style subpackages under `pkg/core/...` were flattened.
