# Execution tools

The worker base must provide **Nix, pnpm (with Node), uv (with a usable Python),
and git**. c2j prepares declared tools and Nix native libraries inside that
environment, including declared Python script environments. It does not build the base image.

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

## Private pnpm installations

An authorized `pnpm:` declaration allows dependency lifecycle builds throughout
its private installation, including transitive and selected optional dependencies.
c2j writes this policy only in its tool-cache project; it does not approve builds
in the managed repository or change user/global configuration. Exact references
remain exact. The bootstrap must support `dangerouslyAllowAllBuilds` (pnpm 10.9
or later). Conflicting build restrictions, script suppression, omitted optional
dependencies and cross-platform overrides fail preparation rather than silently
weakening the policy. Required build failures prevent the operation body from
starting. This does not change the Nix prebuilt-only policy.

Preparation records the bootstrap path, version, launcher type, available lifecycle
Node identity, effective private policy and validation outcome in `setup.tools`.
Failure diagnostics are retained in `_c2j/preparation.json`; raw configuration
output is withheld and installer error text is bounded and redacted.

The exact reference `pnpm:pnpm@12.10.1` additionally requires Linux amd64 or
arm64 with glibc. Cold preparation selects the matching `@pnpm/exe.linux-x64`
or `@pnpm/exe.linux-arm64` dependency and verifies its architecture-specific
archive pin, package/platform metadata and ELF machine type, along with the
pinned wrapper, all four installed aliases and actual `pnpm --version` execution.
Only then is a ready cache record published. Other hosts fail this exact reference
with their observed platform; evidence for one architecture cannot certify another.

pnpm uses a new cache identity and private store/state directories. Older entries
without retained evidence are rebuilt. Warm readiness reads local evidence and
checks hashes, modes, symlink targets, package metadata, policy and generated
bindings without invoking pnpm, Node or a registry. Damaged entries are repaired
under the existing cross-process locks; failed or interrupted preparation cannot
publish a ready entry. Evidence detects cache damage; it does not authenticate
against an actor able to replace both the cache and its evidence.

The build verify hook selects the existing `ubuntu-26.04-arm64` route and retains
required tools and process-provenance checks. Basic worker readiness is separate
from pnpm's offline correctness tests. Real payload preparation checks cold setup,
warm reuse without bootstrap execution, aliases and damaged-cache repair. The
separate offline test uses seccomp/TSYNC in a test subprocess to enforce network
denial and verify cached execution, with before/after connectivity controls.
A passing readiness check does not make an offline failure pass.

The framework runs these tests through the existing hook before merge and owns
verification and publication. The existing Linux amd64 CI row retains matching
native acceptance; arm64 runs the matching payload tests, and other matrix rows
retain portable build-policy tests. Known translation fails required native
acceptance. These tests validate candidate source; the executor operator must
separately establish that a preparation worker runs the published runtime before
claiming activation.

## Python script environments

On Linux and macOS workers, use `python` for libraries imported by scripts. `uv:` continues to install CLI
applications in their own environments; `uv:Pillow` does not make `PIL`
importable by a separate interpreter.

```yaml
id: python-image-check
version: "1.0.0"
input_schema: {}
execution_needs:
  python:
    requires-python: ">=3.10"
    dependencies: ["Pillow==12.0.0"]
op: command_execution
inputs:
  run: |
    python3 - <<'PYTHON'
    from io import BytesIO
    from PIL import Image
    data = BytesIO()
    Image.new("RGB", (2, 2)).save(data, format="PNG")
    data.seek(0)
    with Image.open(data) as image:
        image.load()
        assert image.size == (2, 2)
    PYTHON
```

`requires-python` is an optional standard Python version-specifier string.
`dependencies` is a list of standard Python dependency strings, including extras,
version ranges and environment markers. Use `dependencies: []` for an isolated
interpreter with no libraries; an empty block has the same meaning. Unknown keys,
non-string values and null blocks/lists fail validation. These fields are literal,
not templates. Remote direct references are supported; local file references are
rejected because retained installations are shared across workspaces.

Set repository defaults under `execution.python` in `.c2/config.yaml`. Recipes
can use `execution.python` or `execution_needs.python`; nested nodes use
`execution_needs.python`. Extension manifests use a top-level `python` block.
Go submission overrides and durable execution directives can use
`execution.Requirements.Python`. The closest declaration selects the entire block:
**extension, node/include, job, recipe, repository**. Dependencies and interpreter
constraints do not merge. An empty dependency list replaces inherited libraries;
omitting the block inherits it. Only the selected Python block is prepared.

Preparation asks uv to select a compatible interpreter, creates a virtualenv,
resolves the generated standard metadata, and installs the resolved requirements.
It runs before the command body, under the existing task timeout. Python dependency
syntax and resolution errors come from uv. Source builds are allowed with the
worker's available build tools and any prepared Nix native activation; missing
prerequisites fail preparation. This does not change Nix's prebuilt-only policy.
The worker's `UV_PYTHON_DOWNLOADS` setting remains authoritative. With `never`,
no compatible installed interpreter means failure; a version constraint does not
permit a download. uv configuration-file discovery is disabled for preparation;
worker uv environment settings supply registry and download policy.

The block owns the task's `python` and `python3` bindings even when a separately
declared Nix package exports those names. Its virtualenv bin directory also
exposes installed entry points after explicitly declared CLI bindings. c2j sets
`VIRTUAL_ENV` and `PYTHONNOUSERSITE=1`, and removes `PYTHONHOME`, `PYTHONPATH`, and
`PYTHONUSERBASE`, including conflicting explicit op environment values. Child
processes inherit this environment. Existing CLI tools retain their own
interpreter shebangs. Explicit absolute interpreter paths and scripts that change
their own environment are outside this binding contract.

c2j retains complete environments under `C2J_TOOL_CACHE_DIR/python`, with a
request index and an identity recording the interpreter, platform, resolved
packages and preparation format. Relevant worker resolver settings and Nix native
activation also distinguish installations. Paths are stable and preparation is
locked across processes. Failed or interrupted installs never publish a ready
entry; repair creates a new path without modifying an earlier installation.
Ready environments are immutable: install additional libraries by declaring a
new block, not by running pip against the retained environment.

A warm task performs local file/readiness checks and binds the interpreter; it
does not invoke uv, resolve, download or install. Different cells can share an
entry. Independent cold caches may resolve different compatible versions. The
cached `requirements.txt` and resolved package record preserve each installation;
there is no portable lock or cross-host resolution guarantee in this version.

Keep `UV_CACHE_DIR` and `C2J_TOOL_CACHE_DIR` on the same filesystem for package
reuse between different complete environments. c2j requests hardlinks on Linux
and copy-on-write clones on macOS, with uv's fallback when unavailable. uv caches
source-built wheels too. Declared Nix native environments use separate uv cache
subdirectories so a change in compilers or libraries cannot reuse an incompatible
source-built wheel. Deleting the uv cache does not break ready environments.
Providers own volume retention and separation by tenant, image and platform;
containers must mount retained environments at the same absolute paths. A new
image/cache generation or a host without the shared volume prepares independently.

`setup.python` reports the scope, creation/reuse outcome, elapsed time, actual
interpreter/version, requested and resolved dependencies, and captured command
logs. Installer output uses the existing redacted capture mechanism and its
4 MiB limit per command. Failures retain these diagnostics in
`_c2j/preparation.json` and prevent the command body from running. No `.venv`,
metadata file or lock is written to the managed repository.

For a Nix-only alternative, publish a flake package whose output is
`pkgs.python3.withPackages (ps: [ ps.pillow ])`, and declare that output, for
example `nix:github:example/environments/<commit>#python-with-pillow`. Its complete
closure must already exist locally or in a binary cache. Separately declaring
`python3` and `python3Packages.pillow` is not a supported interpreter/library
composition: native activation does not export Python import paths. Use the
composed output without a `python` block, or use the uv-backed block above.

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
openssl zlib)` and execute the result. Packages may use different pinned nixpkgs
revisions. The shared Nix adapter provides this behavior at
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

Duplicate `pkg-config` metadata is allowed, including metadata from propagated
dependencies. Native inputs use closest-scope precedence, with declaration order
within a scope (first declared first). Identical store inputs are retained at
their closest occurrence. Nix setup hooks construct the resulting metadata,
header, and library search paths in that order; both package versions remain
available. No additional precedence setting is required.

Distinct declared compiler wrappers still fail preparation. Search precedence
does not prove ABI compatibility. Cross-compilation and arbitrary prebuilt binaries
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

Package metadata selects the default, development, binary and library outputs.
c2j validates that complete selection, including the activation input and binary
output, then realizes the sorted, unique evaluated store paths. It does not
request the package derivation again: a wrapper can forbid derivation
substitution while its exact outputs are available from a trusted cache.
Every selected path must have a retained out-link before readiness is published.
Nix retains their referenced closures. Signature requirements and configured
cache trust remain unchanged. Valid `nix-v2` cache entries remain reusable;
missing or retargeted roots invalidate reuse and trigger preparation again.

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
Other pnpm/Nix/uvx commands use the declared executable selected by closest
scope. Same-scope collisions fail explicitly and require a qualified runner.
This corrects earlier behavior that discarded declared manager executables.
When that executable is undeclared, ordinary pnpm/Nix commands use the base
manager normally; ordinary uvx commands require qualification.

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

## Disk exhaustion during setup

If the worker confirms disk exhaustion while preparing tools, before the op
body starts, it stops the launch without recording a failed op. A durable
JobDB resource handoff keeps prior results and gives another compatible worker
the pending attempt. If a resource checkpoint is already selected, or the
backend does not support durable backoff, the worker leaves recovery to lease
expiry and the existing crash budget instead of replenishing it. Operators can
correlate `task.tool_setup.disk_exhausted` with the job, launch ID (when available),
task, affected path, and original diagnostic.
For Nix, disk recovery accepts a terminal write error only from the worker's
isolated prefetch of an explicitly pinned GitHub source. With `NIX_REMOTE=local`,
the worker checks that Nix reports an unqualified `local` URI and the default
local store directories. Custom URIs, directory overrides that change the
layout, and unavailable or unrecognized layout probes bypass prefetch without
changing ordinary setup. Prefetch does not evaluate expressions or run
builders. If it exits normally with an unconfirmed error and checked cleanup
succeeds, ordinary setup uses its caches or reports its own error regardless
of the prefetch exit code. Cancellation, signal termination, and uncertain
cleanup stop setup instead. Evaluation and realization output never authorizes
replay, even if the store is full and the text matches.
A handoff passes the remaining acquired crash allowance unchanged; an unknown
or exhausted allowance leaves recovery to lease expiry.

## Nix integration test

`go test ./...` automatically tests the real installer in a disposable
`nixos/nix:2.35.1` container when Docker is available. It checks binary-cache
installation, qualified execution, warm reuse, and rejection of source builds.
Native tests compile and run against OpenSSL and zlib with implicit and declared
compilers, verify activation after garbage collection and without managers on
PATH, and exercise repository config through a real recipe task.
Signed-cache regressions build an input-addressed, substitution-disabled wrapper
in a separate producer, then copy its signed cache to independent cold consumers.
They compare the old derivation request with exact-path preparation, reject
missing/untrusted outputs and missing closure members, and verify all selected
outputs and payload execution survive collection while an unrooted sentinel is
removed. They also check manager-free offline reuse, damaged-root repair, disabled
import-from-derivation and failure before a repository command body executes.
Private fixture keys remain in the disposable producer.

`TestRealC2opsEnvironment` uses all fourteen declarations from c2ops hook revision
`36c8a3c70cfa8179e445115e635820c97675f4eb`, including native activation, executable
bindings and warm reuse. It requires Linux aarch64. The candidate hook also runs
this test on its actual worker with `C2J_TEST_C2OPS_LOCAL=1`; it does not collect
garbage in the worker store. These package checks do not replace a fresh complete
c2ops hook invocation through the existing nonpublishing merge-check entrypoint
after runtime activation.
The test needs network access and skips when Docker is unavailable or `-short`
is set. To run the package tests alone:
`go test ./pkg/toolenv -run 'TestReal(Nix|NativeEnvironment|C2opsEnvironment)' -v -timeout=20m`.
For the repository-to-command test:
`go test ./pkg/worker/compiler -run TestRealRepositoryNativePackages -v`.
The implementation was validated on Linux ARM64 with Nix 2.35.1; other
platforms/toolchains need their own validation.

## Python verification

`go test ./pkg/toolenv -run TestPython -count=1` uses real uv and Python when
available, serving test wheels and a source distribution locally. CI installs
uv 0.13.6 and Python 3.13 so these tests run on each platform in the matrix. It covers
scope selection, concurrent creation, warm reuse with uv disabled, package-cache
cleanup, source-build reuse, linked files, interrupted installs and incompatible
requests. It needs no public registry. Recipe/worker tests cover scope restoration,
extension overrides, replay and failure-before-body diagnostics.

The optional registry smoke test installs Pillow and creates, saves and decodes a
PNG through the selected interpreter:

```sh
C2J_TEST_REAL_PYTHON=1 go test ./pkg/toolenv -run TestRealPythonPillow -count=1 -v
```

Local tests do not establish provider rollout. Before rollout, rebuild base2 with
the feature, update Pulse's c2j library dependency, and verify both published
architectures with the actual shared mounts. Record image digests and c2j/uv/Python
versions; verify separate-container reuse, controller restart and cache-generation
rotation. The adaptive3 validator and package-probe integration remain acceptance
checks for that deployed environment.
