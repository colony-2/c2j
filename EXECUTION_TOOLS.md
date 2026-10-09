# Execution tools

The worker base must provide **Nix, pnpm (with Node), uv (with a usable Python),
and git**. c2j prepares declared CLI tools; it does not build the base image.

```yaml
execution:
  packages:
    - nix:github:NixOS/nixpkgs/<commit>#jq
    - uv:ruff==0.11.2
sequence:
  - id: check
    execution_needs:
      packages: [pnpm:typescript@5.8.3]
    op: command_execution
    inputs:
      run: ruff check . && tsc --noEmit
```

An extension's `op.yaml` uses the same references:

```yaml
dependencies: [uv:ruff==0.11.2]
```

References are literal strings, split at the first colon. Package syntax belongs
to the selected manager; use explicit versions or Nix revisions for repeatable
resolution. These declarations install CLI applications, not dependencies into
the task's project or Python environment.

Nix setup uses only existing store outputs or prebuilt packages from configured
binary caches (`--max-jobs 0 --builders ''`, including metadata evaluation).
If a required output needs building, setup fails before the op starts.

Recipe and node declarations are inherited. Tools are prepared when execution
first reaches an uncached task in their scope; op dependencies are prepared
before that op. Cached replay reads recorded setup results without accessing
package managers or extension sources. Unvisited ops are not resolved. Existing
recipe snapshot/include loading and CEL imports still provide the structure and
functions needed to traverse a recipe.

Each invocation gets its own PATH. The nearest scope wins (op dependency, node,
job override, recipe); versions coexist. A same-scope executable collision requires
one of these qualified forms:

```sh
uvx --from 'ruff==0.11.2' ruff check .
pnpm --package=typescript@5.8.3 dlx tsc --version
nix run 'github:NixOS/nixpkgs/<commit>#jq' -- --version
```

For declared tools, c2j binds these forms to retained executables so timed execution
cannot trigger installation. They support one package and an explicit executable
for uvx/pnpm; `nix run` requires a derivation with a discoverable main program.
Other pnpm/Nix commands use the base manager normally.

Setup has a separate 30-minute invocation limit and runs before the op's timeout
starts. Enclosing recipe/sequence deadlines still apply. If a prepared environment
is missing before a live step, setup runs again and resumes that step, preserving
completed steps. Setup failures prevent execution and remain in job history.
Task output envelopes contain `setup.wall_ms`, per-tool scope/identity/outcome,
and `setup.task_ordinal`, which links to the durable setup record. Count each setup
ordinal once when aggregating across steps or retries; user op outputs are unchanged.

## Local reuse and providers

Set `C2J_TOOL_CACHE_DIR` to an absolute, writable cache directory; the default is
`$XDG_CACHE_HOME/c2j/tools` (normally `~/.cache/c2j/tools`). c2j uses isolated uv tool
installations, pnpm projects, and GC-rooted Nix outputs. Completed installations
are reused without manager/version lookups; concurrent preparation is locked.

Providers own persistence and eviction. For Docker, retain this directory plus
`/nix/store` and its GC roots at stable paths, with matching worker permissions.
Keep caches separate across incompatible base/runtime versions and trust domains.
Mounting only download caches still requires installation. No image-building or
Pulse-provider implementation is included here. An unpinned reference may resolve
to a newer version after its installation is evicted; there is no transitive
package lock service.

New submissions record `tool_setup_version: 1` in their internal job input. Older
jobs retain their original extension-resolution history for replay compatibility.
Direct JobDB integrations that construct `StartJob` themselves must set this field
to enable the new extension dependency lifecycle.
