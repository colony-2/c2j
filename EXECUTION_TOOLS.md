# Execution tools

The worker base must provide **Nix, pnpm (with Node), uv (with a usable Python),
and git**. c2j prepares declared tools and Nix native libraries inside that
environment. It does not build the base image.

Declare dependencies needed by every operation on a repository in its existing
`.c2/config.yaml`:

```yaml
packages:
  - nix:nixpkgs#go
  - pnpm:npm@10.9.2
  - pnpm:playwright@1.51.1
  - uv:ruff==0.11.2
```

These versions illustrate syntax. Pin versions and Nix revisions for repeatable
resolution. Executables are available on each operation's `PATH`. Repository
packages use the same references as recipe, node, and extension declarations:

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

Ops can also be distributed as prebuilt Nix packages, with metadata inspection
through `passthru.c2j` and payload retrieval only after an activity-result miss.
See the [extension op guide](EXTENSION_OPS.md) for the package and manifest contract.

References are literal strings, split at the first colon. Package syntax belongs
to the selected manager. pnpm and uv install isolated CLI applications; they do
not add importable dependencies to a project's Node or Python environment.
Playwright's CLI declaration does not provision browser binaries or all browser
system dependencies.

## Repository configuration

Each live task reads `.c2/config.yaml` from its restored workspace snapshot,
after input authorization and before installing packages or running the op.
Only that workspace's file contributes repository packages: ancestor configs,
the submitter's checkout, and repositories supplying included recipes do not.
A workspace switch selects the new repository's list. A config edit takes
effect on the next task consuming the edited snapshot; retries start from their
original input snapshot.

A missing file, missing field, `null`, or `[]` declares no repository packages.
Exact duplicates are removed. Invalid YAML, non-string entries, unknown
managers, and symlinked config files/directories fail preparation. Loading reads
package data only, without evaluating templates or config commands. Repository
references must be portable registry or remote references; local paths,
`file:`/`link:`/`workspace:` dependencies, and local flakes such as `nix:.#tool`
are rejected because installations run in the cache, not in the repository.

`c2j self` displays the local config path and packages; `c2j init` includes a
commented example. Recipe-test passthrough uses its designated workspace's
config. Fully mocked tests skip package discovery and installation.

## Native libraries

Nix packages may provide headers, libraries, setup hooks, and executables:

```yaml
packages:
  - nix:nixpkgs#stdenv.cc
  - nix:nixpkgs#pkg-config
  - nix:nixpkgs#openssl
  - nix:nixpkgs#zlib
```

An operation can then run `cc probe.c -o probe $(pkg-config --cflags --libs
openssl zlib)` and execute the result. Use a common pinned nixpkgs revision for
the toolchain and libraries. The shared Nix adapter provides this behavior at
every declaration site, including recipe/node packages and op dependencies.

c2j retains development, runtime, and executable outputs, including libraries
with no `bin` directory. It activates Nixpkgs compiler wrappers and setup hooks
in a clean subprocess, then caches the supported exported variables. Those
include compiler tools, `NIX_*` wrapper flags, `PKG_CONFIG_PATH`, and CMake
discovery paths. Arbitrary shell functions, `shellHook`, credentials, and
temporary build paths are not carried into operations. Explicit op environment
values override scalar defaults; executable bindings remain first on `PATH`.

Activation uses nixpkgs revision
`b6018f87da91d19d0ab4cf979885689b469cdd41` for its standard environment and
pkg-config support. It supplies a default C compiler when none is declared;
a declared compiler wrapper such as `stdenv.cc` selects the compiler instead.
CLI packages that need no native activation retain the lightweight PATH setup.

Distinct declared compiler wrappers or conflicting `pkg-config` modules fail
preparation. Executable scope precedence does not isolate incompatible native
libraries; use separate invocations for those. Other header/ABI conflicts cannot
all be detected automatically. Cross-compilation and arbitrary prebuilt binaries
requiring a different libc/loader are outside this contract. Nix-linked programs
use their runtime paths; c2j does not inject a global `LD_LIBRARY_PATH`. Software
loading plugins by bare name may need an explicit loader path or package wrapper.

Nix setup uses only existing store outputs or prebuilt packages from configured
binary caches (`--max-jobs 0 --builders ''`, including metadata evaluation).
Import-from-derivation is disabled. Native activation realizes the generated
shell's inputs and sources the retained standard environment; it never builds
the shell or a `print-dev-env` capture derivation. If a required output needs
building, setup fails before the op starts. Legacy and version-4 derivation JSON
are supported; structured attributes and dynamic outputs fail explicitly.

## Scope and execution

Recipe and node declarations are inherited. Tools are prepared when execution
first reaches an uncached task in their scope; op dependencies are prepared
before that op. Cached replay reads recorded setup results without accessing
package managers or extension sources. Unvisited ops are not resolved. Existing
recipe snapshot/include loading and CEL imports still provide the structure and
functions needed to traverse a recipe.

Each invocation gets its own PATH and native environment. The nearest scope wins
(op dependency, node, job override, recipe, repository); versions coexist. Every
declared package must prepare, even when a nearer scope overrides an executable.
A same-scope executable collision requires
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

Setup runs inside each live task after workspace restore and before its op body.
The resulting environment applies to the op and its subprocesses; worker Git
restore/persistence uses the base environment. Preparation and execution
currently share the task timeout; enclosing deadlines still apply. Cached tasks
skip setup entirely. Lost installations are prepared again for the next live
step without repeating completed steps.

Task outcomes contain `preparation_ms`, `setup.wall_ms`, per-tool
scope/identity/outcome, and `setup.task_ordinal` identifying the consuming task.
`preparation_ms` includes workspace restore. `setup.repository` records config
path, digest, normalized packages, workspace scope, repository, incoming snapshot,
and whether the file was missing. `setup.native` records composed activation
identity, timing, and cache outcome. The config digest is provenance, not a lock.
Worker-reported failures attach diagnostics as `_c2j/preparation.json` outcome
artifacts. No separate setup task is created. A future JobDB preparation phase
can give setup its own timeout.

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

This execution format targets fresh databases; there is no legacy setup-history
compatibility path or `tool_setup_version` switch.

## Nix integration test

`go test ./...` automatically tests the real installer in a disposable
`nixos/nix:2.35.1` container when Docker is available. It checks binary-cache
installation, qualified execution, warm reuse, and rejection of source builds.
Native tests compile and run against OpenSSL and zlib with implicit and declared
compilers, verify activation after garbage collection and without managers on
PATH, and exercise repository config through a real recipe task.
The test needs network access and skips when Docker is unavailable or `-short`
is set. To run the package tests alone:
`go test ./pkg/toolenv -run 'TestReal(Nix|NativeEnvironment)' -v`.
For the repository-to-command test:
`go test ./pkg/worker/compiler -run TestRealRepositoryNativePackages -v`.
The implementation was validated on Linux ARM64 with Nix 2.35.1; other
platforms/toolchains need their own validation.
