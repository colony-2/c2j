# Extension Op Selector-in-`op` Plan

## Goal

Make extension ops directly referenceable from recipes without preregistration, while keeping the existing `op:` field.

This change should cover:

- `codex.exec`
- `llm_inference`
- `llm_inference2`
- the GitHub Actions op

Under this model, `op` can be either:

1. a bare registered core op name
2. a same-repo local selector
3. a canonical remote git selector

Examples:

```yaml
- id: ask_user
  op: input
  inputs:
    schema:
      type: object
```

```yaml
- id: implement_local
  op: ./server/llm/pkg/codex/extension-codex-exec
  inputs:
    prompt: "Implement the approved plan."
```

```yaml
- id: implement_remote
  op: git+https://github.com/acme/platform-ops.git//tools/extensions/codex.exec@main
  inputs:
    prompt: "Implement the approved plan."
```

## Why Keep `op:`

This is cleaner than introducing `uses:` because:

- it preserves the current recipe surface
- it reuses the selector idea already present elsewhere in the system
- it keeps parse behavior simple:
  1. exact registered op name
  2. local selector
  3. canonical git selector
  4. otherwise error

The tradeoff is that `op:` is no longer registry-only, but that is still simpler than adding another node family.

## Supported Selector Forms

## 1. Bare registered core op

Example:

```yaml
op: input
```

Semantics:

- exact match against the current global registry
- current behavior unchanged

## 2. Same-repo local selector

Example:

```yaml
op: ./server/llm/pkg/codex/extension-codex-exec
```

Semantics:

- resolve relative to the caller repo root
- bind to the same effective repo+commit as the caller recipe
- materialize from that same commit, not from an arbitrary live checkout

This should be supported from the start.

Reason:

- it makes testing much easier
- it allows extension bundle iteration inside the same repo
- it matches the ergonomics of same-repo reusable building blocks in systems like GitHub Actions

## 3. Canonical remote git selector

Example:

```yaml
op: git+https://github.com/acme/platform-ops.git//tools/extensions/codex.exec@main
```

Canonical shape:

`git+<scheme>://<repo-location>//<repo-relative-op-dir>@<git-ref>`

Semantics:

- validate selector
- resolve mutable refs to concrete commits
- record the resolved commit-pinned selector
- materialize the target extension bundle

I recommend using this canonical form as the remote syntax because:

- the repo already has a real parser/resolver for canonical git selectors in recipe root loading
- it avoids ambiguity with bare op names
- it keeps selector grammar uniform across features

## Current Constraint

Today the recipe system assumes `op:` resolves through the global registry:

- parse-time unknown-op validation comes from the registry
- schema generation enumerates registered ops

Relevant code:

- [ops.go](/src/pkg/core/pkg/ops/ops.go)
- [schema.go](/src/pkg/core/pkg/recipe/schema.go)
- [node.go](/src/pkg/core/pkg/recipe/node.go:74)

To support selector-backed `op:` values, that assumption needs to widen to:

1. `op` is a registered core op name, or
2. `op` is a valid local selector, or
3. `op` is a valid remote selector

## Proposed Parse / Compile Semantics

For each node:

1. Read `op` as a string.
2. If it exactly matches a registered op, keep the existing path.
3. Else if it starts with `./`, resolve it as a same-repo local selector.
4. Else if it parses as a canonical `git+...//path@ref` selector, resolve it as a remote extension selector.
5. Else return the current unknown-op style error.

## Bundle Layout

The selector points to an op directory.

Recommended layout:

```text
server/llm/pkg/codex/extension-codex-exec/
  op.yaml
  run
  ...
```

`op.yaml` should define:

- `name`
- `description`
- `version`
- `run` or `command` / `args`
- `working_directory`
- `env`
- `timeout`
- `input_schema`
- `output_schema`

Sandbox is intentionally not part of the bundle manifest.

## Reserved Runtime Inputs

Selector-backed extension ops should support reserved runtime inputs handled by the extension runtime rather than by the extension bundle itself.

The first reserved runtime input should be:

- `sandbox`

Suggested shape:

```yaml
sandbox:
  type: none | shai
  inline_config:
    type: shai-sandbox
    version: 1
    image: ghcr.io/colony-2/shai-mega
```

Semantics:

- omitted `sandbox` means run on host
- `sandbox.type=none` means run on host with no additional sandboxing
- `sandbox.type=shai` means run in Shai
- when running in Shai, the effective config is always:
  - local config
  - plus any `inline_config` overlay

There is no `source` property in this model.

Important validation rule:

- the runtime must separate reserved runtime inputs like `sandbox` from extension-defined inputs before validating against the bundle's `input_schema`

That way bundle authors do not need to repeat the sandbox schema in every manifest.

## Local Shai Config Resolution

When `sandbox.type=shai`, the runtime should resolve local config from the current execution context.

Recommended lookup:

1. use `deps.WorktreePath()` and `deps.GitContext().CellPath`
2. check `<worktree>/<cell_path>/.shai/config.yaml`
3. optionally walk upward toward repo root for nearest `.shai/config.yaml`
4. fail clearly if none found

The effective Shai config is:

1. resolved local config
2. merged with any `sandbox.inline_config`

## Complete Examples

The examples below show:

- what lives in the extension bundle
- how sandboxing is chosen at invocation time through `inputs.sandbox`
- how recipes refer to extensions through `op:`

## Example 1: Host execution, no sandbox

Bundle layout:

```text
tools/extensions/text_stats/
  op.yaml
  main.py
```

`op.yaml`:

```yaml
name: text_stats
description: Count lines, words, and bytes in a text payload.
version: 1.0.0
command: ["python3", "main.py"]
working_directory: .
timeout: 30s
input_schema:
  type: object
  required: [text]
  properties:
    text:
      type: string
output_schema:
  type: object
  required: [lines, words, bytes]
  properties:
    lines:
      type: integer
    words:
      type: integer
    bytes:
      type: integer
```

`main.py`:

```python
import json
import sys

payload = json.load(sys.stdin)
text = payload["text"]

print(json.dumps({
    "lines": len(text.splitlines()),
    "words": len(text.split()),
    "bytes": len(text.encode("utf-8")),
}))
```

Recipe usage:

```yaml
- id: summarize_text
  op: ./tools/extensions/text_stats
  inputs:
    text: |
      alpha
      beta gamma
```

Explicit no-sandbox variant:

```yaml
- id: summarize_text
  op: ./tools/extensions/text_stats
  inputs:
    text: hello
    sandbox:
      type: none
```

## Example 2: Shai sandbox with inline config overlay

Bundle layout:

```text
tools/extensions/go_test/
  op.yaml
  runner.py
```

`op.yaml`:

```yaml
name: go_test
description: Run go test for a package.
version: 1.0.0
command: ["python3", "runner.py"]
working_directory: .
timeout: 10m
input_schema:
  type: object
  required: [package]
  properties:
    package:
      type: string
output_schema:
  type: object
  required: [ok, report_path]
  properties:
    ok:
      type: boolean
    report_path:
      type: string
```

`runner.py`:

```python
import json
import os
import subprocess
import sys

payload = json.load(sys.stdin)
pkg = payload["package"]

worktree = os.environ["VIBETHIS_WORKTREE_PATH"]
outbox = os.environ["VIBETHIS_ARTIFACT_OUTBOX"]
report_name = "go-test-report.txt"
report_path = os.path.join(outbox, report_name)

proc = subprocess.run(
    ["go", "test", pkg],
    cwd=worktree,
    capture_output=True,
    text=True,
)

with open(report_path, "w", encoding="utf-8") as f:
    f.write(proc.stdout)
    if proc.stderr:
        f.write("\n--- stderr ---\n")
        f.write(proc.stderr)

print(json.dumps({
    "ok": proc.returncode == 0,
    "report_path": report_name,
}))

if proc.returncode != 0:
    sys.exit(proc.returncode)
```

Recipe usage:

```yaml
- id: run_go_tests
  op: git+https://github.com/acme/platform-ops.git//tools/extensions/go_test
  inputs:
    package: ./server/ops/...
    sandbox:
      type: shai
      inline_config:
        type: shai-sandbox
        version: 1
        image: ghcr.io/colony-2/shai-mega
        resources:
          default:
            mounts:
              - source: ${{ env.HOME }}/.cache/go-build
                target: /home/shai/go/pkg
                mode: rw
              - source: ${{ env.HOME }}/.cache/go-mod
                target: /home/shai/go/mod
                mode: rw
            http:
              - proxy.golang.org
              - sum.golang.org
              - go.dev
```

This means:

- the invocation opts into Shai with `sandbox.type=shai`
- the runtime resolves local config first
- the runtime applies the inline overlay second

## Example 3: Shai sandbox using only local config

Bundle layout:

```text
tools/extensions/cell_lint/
  op.yaml
  runner.py
```

`op.yaml`:

```yaml
name: cell_lint
description: Run the cell's lint command.
version: 1.0.0
command: ["python3", "runner.py"]
working_directory: .
timeout: 10m
input_schema:
  type: object
  properties:
    command:
      type: array
      items:
        type: string
output_schema:
  type: object
  required: [ok, report_path, cell_path]
  properties:
    ok:
      type: boolean
    report_path:
      type: string
    cell_path:
      type: string
```

`runner.py`:

```python
import json
import os
import subprocess
import sys

payload = json.load(sys.stdin)
command = payload.get("command", ["bash", "-lc", "make lint"])

worktree = os.environ["VIBETHIS_WORKTREE_PATH"]
cell_path = os.environ["VIBETHIS_CELL_PATH"]
outbox = os.environ["VIBETHIS_ARTIFACT_OUTBOX"]

cell_root = os.path.join(worktree, cell_path)
report_name = "lint-report.txt"
report_path = os.path.join(outbox, report_name)

proc = subprocess.run(
    command,
    cwd=cell_root,
    capture_output=True,
    text=True,
)

with open(report_path, "w", encoding="utf-8") as f:
    f.write(proc.stdout)
    if proc.stderr:
        f.write("\n--- stderr ---\n")
        f.write(proc.stderr)

print(json.dumps({
    "ok": proc.returncode == 0,
    "report_path": report_name,
    "cell_path": cell_path,
}))

if proc.returncode != 0:
    sys.exit(proc.returncode)
```

Recipe usage:

```yaml
- id: lint_current_cell
  op: ./tools/extensions/cell_lint
  inputs:
    command: ["bash", "-lc", "npm test"]
    sandbox:
      type: shai
```

This means:

- the invocation opts into Shai
- no inline override is provided
- the runtime uses the locally resolved Shai config only

## Resolution Model

## Remote selector

For:

```yaml
op: git+https://github.com/acme/platform-ops.git//tools/extensions/codex.exec@main
```

Resolution should:

1. parse selector
2. clone/fetch repo into cache
3. resolve `@main` to a concrete commit
4. compute the resolved selector:
   - `git+https://github.com/acme/platform-ops.git//tools/extensions/codex.exec@main@<commit>`
5. materialize the target directory for that exact commit
6. load `op.yaml`

## Local selector

For:

```yaml
op: ./server/llm/pkg/codex/extension-codex-exec
```

Resolution should:

1. determine the effective repo+commit of the current recipe
2. resolve the relative path in that same repo
3. materialize from that same commit, not from an arbitrary live checkout

If the root recipe was itself loaded from a pinned remote selector, local extension selectors should inherit that same resolved source identity.

## Cache Design

Use a local cache under:

- `~/.c2/cache/ops`

Recommended layout:

```text
~/.c2/cache/ops/
  repos/
    <repo-key>/
  bundles/
    <repo-key>/<commit>/<bundle-key>/
  metadata/
    ...
```

### Repo cache

`repos/<repo-key>` stores the local clone or mirror.

Purpose:

- avoid recloning
- support fetch/refresh for mutable refs

### Bundle cache

`bundles/<repo-key>/<commit>/<bundle-key>` stores the exact materialized op directory.

Purpose:

- immutable by commit
- reusable across runs
- cheap cache-hit semantics

### Cache keys

- `repo-key`: stable hash of normalized repo URL
- `bundle-key`: stable hash of repo-relative op path

## Runtime Execution Model

There should still be one generic extension runtime in `server/ops`, but it is an internal implementation detail behind selector-backed `op:` values.

Runtime flow:

1. detect selector-backed `op`
2. resolve selector to concrete commit
3. materialize the bundle from cache or git
4. load `op.yaml`
5. separate reserved runtime inputs from extension-defined inputs
6. validate extension-defined inputs against `input_schema` if present
7. export runtime env values
8. execute on host or in Shai according to `inputs.sandbox`
9. parse stdout JSON
10. validate against `output_schema` if present

## Schema and Validation Changes

## 1. Static recipe schema

The static schema should no longer insist that every `op` be one of the registered op consts only.

Instead, `op` should accept:

- a known built-in op name
- a canonical git selector string
- a `./...` local selector string

The static schema can still be strong because selector strings have recognizable prefixes.

## 2. Parse-time validation

Validation becomes:

1. if `op` matches a registered core op, validate as today
2. else if `op` starts with `./`, continue to local-selector extension validation
3. else if `op` parses as a canonical `git+...//path@ref` selector, continue to remote-selector extension validation
4. else error

## 3. Dynamic extension validation

After selector resolution:

1. load extension `input_schema`
2. validate extension-defined `inputs`
3. handle reserved runtime fields like `inputs.sandbox`
4. compile execution for that bundle

## Shared Resolver

Do not create a new git ref subsystem just for extension ops.

The repo already has a real canonical git-selector implementation for recipe roots in:

- [root_source.go](/src/pkg/worker/pkg/compiler/root_source.go)

Recommendation:

- extract the generic selector parse/resolve pieces into a shared package
- use that shared package for selector-backed `op:` values

That gives:

- one canonical parser
- one commit-resolution policy
- one place for fetch/clone semantics

## Runtime Environment

The runtime should export at least:

- `VIBETHIS_WORKDIR`
- `VIBETHIS_WORKTREE_PATH`
- `VIBETHIS_ARTIFACT_INBOX`
- `VIBETHIS_ARTIFACT_OUTBOX`
- `VIBETHIS_CELL_PATH`
- `VIBETHIS_NODE_PATH`
- `VIBETHIS_PROJECT_ROOT`
- `VIBETHIS_OP_DIR`
- `VIBETHIS_OP_NAME`
- `VIBETHIS_INPUT_JSON`

## `command_execution` Should Reuse The Same Sandbox Model

As part of this change, the built-in `command_execution` op should support the same sandbox behavior as selector-backed extension bundles.

That means `command_execution` should support:

- omitted `sandbox` means host execution
- `sandbox.type=none`
- `sandbox.type=shai`
- optional `sandbox.inline_config`

Suggested recipe shape:

```yaml
- id: run_tests
  op: command_execution
  inputs:
    run: go test ./server/ops/...
    sandbox:
      type: shai
```

Inline-config example:

```yaml
- id: run_scan
  op: command_execution
  inputs:
    run: python3 scripts/scan.py
    sandbox:
      type: shai
      inline_config:
        type: shai-sandbox
        version: 1
        image: ghcr.io/colony-2/shai-mega
```

Explicit host example:

```yaml
- id: local_echo
  op: command_execution
  inputs:
    run: echo hello
    sandbox:
      type: none
```

Implementation note:

- `command_execution` should not keep a one-off sandbox implementation
- it should call the same reusable sandbox resolution + execution layer used by selector-backed extension bundles

## Codex / LLM / GHA Migration Under This Model

Under this design, `codex.exec`, `llm_inference`, `llm_inference2`, and the GitHub Actions op stop being statically registered names.

Instead, recipes reference them directly through selector-backed `op:` values.

Examples:

```yaml
op: ./server/llm/pkg/codex/extension-codex-exec
```

```yaml
op: git+https://github.com/acme/platform-ops.git//tools/extensions/codex.exec@main
```

The bundles should preserve current contracts.

### Codex bundle

Must preserve:

- current input/output behavior
- session resume
- `stdout.jsonl` and `stderr.txt` in outbox
- skills materialization

### LLM bundles

Must preserve:

- existing request/response contracts
- response-schema validation
- file/tool behavior for `llm_inference2`

### GHA bundle

The GitHub Actions op should also shift to an extension bundle as part of this change.

Must preserve:

- current workflow invocation contract
- current output/result shape expected by downstream recipes
- current artifact and metadata behavior
- selector-resolution behavior that remains specific to the GitHub Actions domain

Important:

- the GitHub Actions op becoming an extension bundle does not mean workflow selectors and extension selectors become the same thing
- it means the outer op implementation is delivered as a selector-backed extension bundle, while its own internal workflow input semantics continue to do whatever the GHA feature needs

## Recommended First Slice

1. Make `op:` accept local selectors and canonical remote git selectors in addition to bare names.
2. Reuse or extract the existing git-selector resolver.
3. Add local cache under `~/.c2/cache/ops`.
4. Add bundle materialization.
5. Add generic host/Shai execution controlled by `inputs.sandbox`.
6. Apply the same sandbox runner to `command_execution`.
7. Migrate `codex.exec` first.

Why `codex.exec` first:

- it already requires worktree/inbox/outbox/cell-path semantics
- it already has a Shai path to preserve
- it is the best stress test of the new runtime

## Phased Plan

## Phase 1: `op:` selector support

1. Widen recipe parsing so `op` can be a registered name, a local selector, or a remote selector.
2. Add selector detection for `./...`.
3. Add selector detection for canonical `git+...//path@ref`.
4. Add resolved-selector metadata where needed for traceability.

Acceptance criteria:

- `op: input` still works unchanged
- `op: ./<repo-relative-op-dir>` is accepted and resolved
- `op: git+...//path@ref` is accepted and resolved

## Phase 2: Cache and bundle resolution

1. Add repo cache under `~/.c2/cache/ops/repos`.
2. Add bundle cache under `~/.c2/cache/ops/bundles`.
3. Resolve refs to commits and materialize exact bundle directories.
4. Bind local selectors to the same effective repo+commit as the caller.

Acceptance criteria:

- repeated use of the same resolved selector reuses cache
- local selectors work cleanly in tests and in normal recipes

## Phase 3: Dynamic validation and execution

1. Read `op.yaml`.
2. Separate reserved runtime inputs from extension-defined inputs.
3. Validate extension-defined inputs against the bundle schema.
4. Implement host execution.
5. Implement Shai execution.
6. Export runtime env values.
7. Reuse the same runner for `command_execution`.

Acceptance criteria:

- selector-backed `op` values execute without prior registration
- sandbox behavior is controlled by invocation inputs, not bundle metadata

## Phase 4: Codex migration

1. Package codex as an extension bundle in a repo.
2. Invoke it through selector-backed `op:`.
3. Preserve artifacts and resume semantics.
4. Port integration coverage.

Acceptance criteria:

- codex behavior matches current behavior through selector-backed `op`

## Phase 5: LLM and GHA migration

1. Package `llm_inference` and `llm_inference2` as extension bundles.
2. Invoke them through selector-backed `op:`.
3. Package the GitHub Actions op as an extension bundle.
4. Preserve validation and execution behavior.

Acceptance criteria:

- llm bundles and the GitHub Actions bundle run correctly through selector-backed `op`

## Optional Later Work

- allowlists / policy controls for approved selectors
- cache pruning
- migration of skill refs to the same canonical selector grammar

## Testing Plan

### Unit tests

- selector detection vs bare-name detection
- local selector parsing
- canonical git-selector parsing
- commit-pinned resolution
- local-selector same-commit binding
- cache key generation
- local-config Shai resolution

### Integration tests

- `op: input` still works
- `op: ./<repo-relative-op-dir>` works in-repo
- `op: git+file://...//<repo-relative-op-dir>` works against local test repos
- host execution
- Shai execution
- `command_execution` host/Shai parity
- cache hit vs miss
- codex bundle through selector-backed `op`
- GHA bundle through selector-backed `op`

### Regression tests

- static core-op schema generation still works
- parse-time unknown-op errors still work for bad non-selector strings
- selector-backed `op` values get dynamic post-resolution validation

## Direct Answer

I think this is the right direction:

- keep `op:`
- make it either a bare registered name, a same-repo local selector, or a canonical git selector
- support same-repo local selectors from the start
- keep sandbox configuration on the invocation input, not in the bundle manifest

That is more consistent with the existing system than adding `uses:`, and it avoids inventing another reusable-reference mechanism.

## Definition of Done

This is done when:

1. `op:` accepts either a registered core op name, a same-repo local selector, or a canonical git selector
2. selector-backed `op` values resolve to commit-pinned extension bundles
3. local selectors bind to the caller's effective repo+commit
4. bundles are cached under `~/.c2/cache/ops`
5. bundles execute without prior registration
6. bundle sandbox behavior is controlled through `inputs.sandbox`
7. `command_execution` supports the same sandbox model
8. `codex.exec`, `llm_inference`, `llm_inference2`, and the GitHub Actions op can all be consumed this way
