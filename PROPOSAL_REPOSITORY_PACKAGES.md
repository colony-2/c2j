# Repository packages in `.c2/config.yaml`

Status: implemented and tested. This document records the design;
[Execution tools](EXECUTION_TOOLS.md) describes the shipped behavior. The
[prototype](prototypes/nix-native-environment/README.md) records the initial
experiment, now covered by production integration tests.

Add a repository-level `packages` list to `.c2/config.yaml`. Before a live c2j
operation runs against that repository, prepare its declared packages, expose
executables on the operation's `PATH`, and activate Nix native-library build
dependencies. Reuse the package references, installation cache, scope precedence,
and diagnostics already used by recipes and extension ops. Extend the shared
environment adapter to support library activation at every declaration site.

The file is `.c2/config.yaml`, the existing project configuration path; this
proposal does not introduce a second file named `.c2/config`.

## Configuration

```yaml
# .c2/config.yaml
base: go
packages:
  - nix:nixpkgs#go
  - pnpm:npm@10.9.2
  - pnpm:playwright@1.51.1
  - uv:ruff==0.11.2
```

These versions illustrate the syntax, not recommended current releases. This
configuration makes `go`, `npm`, `npx`, `playwright`, and `ruff` available to
c2j-launched operation processes and their descendants. Authors do not need to
repeat the list in every recipe. Tools are installed in retained, isolated
locations accessible inside the execution container; they are not installed
globally into `/usr/bin` or activated for unrelated container processes.

Use one ordered list of manager-prefixed strings, exactly as in recipe
`execution.packages`, node `execution_needs.packages`, and extension manifest
`dependencies`. A top-level `packages` field fits the existing flat project
configuration without implying support for repository image/resource defaults.
The existing `dependents` field continues to describe dependent cells.

| Rule | Behavior |
| --- | --- |
| Missing config, omitted `packages`, `null`, or `[]` | No repository package requirements. |
| Entry format | Literal string parsed by `execution.ParsePackage`; split at the first colon. |
| Managers | `nix`, `pnpm`, and `uv`; there is no `npm:` prefix. |
| Versions | Native manager syntax, including scoped names such as `pnpm:@scope/tool@1.2.3`. |
| Duplicate references | Deduplicate exact strings within the scope, retaining first occurrence order. |
| Different versions | Retain both references; executable selection follows the existing scope rules. |
| Invalid shape/reference | Error naming the file and index, for example `.c2/config.yaml: packages[2]: unsupported package prefix "npm"`. |
| Evaluation | No templates, commands, environment expansion, or network resolution during parsing. |

Reject non-string entries rather than allowing YAML to coerce them into strings.
Reuse the shared package validator, including its rejection of empty specs,
leading option syntax, line breaks, and template expressions. Unknown existing
project fields retain the project's current decoding policy; this feature does
not make the whole configuration schema strict.

Pin registry versions and Nix revisions when repeatability matters, for example
`nix:github:NixOS/nixpkgs/<commit>#go`. An unpinned reference retains the existing
cache semantics: eviction can cause it to resolve differently. This feature
does not add a package lockfile or make registry dependencies fully reproducible.

## Existing contracts to reuse

The implemented behavior in [Execution tools](EXECUTION_TOOLS.md) and
[Configuration in task inputs](CONFIGURATION.md) is the baseline. Native-library
activation described below is an explicit extension to that baseline. The earlier
[execution environment proposal](PROPOSAL_EXECUTION_ENVIRONMENT_ADDONS.md)
provides background, but its separate setup-task and timeout-exclusion design
has been superseded: preparation runs inside the consuming task and shares its
timeout.

| Existing component | Reuse |
| --- | --- |
| [Project configuration](pkg/config/config.go) | Decode and expose the new field alongside existing project fields. |
| [Package references](pkg/execution/packages.go) | One parser for all declaration sites. |
| [Execution requirements](pkg/execution/requirements.go) | Existing additive, exact-reference deduplication semantics. |
| [Tool environments](pkg/toolenv/environment.go) | `Scope`, `Manager.Prepare`, isolated installs, locks, readiness, executable bindings, and qualified runners. |
| [Invocation preparation](pkg/worker/ops/preparation.go) | Combine repository, recipe/node/job, and op requirements before installation. |
| [Process runtime](pkg/ops/process/runtime.go) | `WithToolPath` prepends invocation-local bindings without changing the worker's environment. |

Extend the existing Nix adapter and prepared environment rather than introducing
a second package system or provisioning service. Packages remain worker
preparation requirements, not proof
that an executor's image/platform/resource allocation is compatible. Preserve
the [portable execution requirements contract](C2J_PORTABLE_EXECUTION_REQUIREMENTS_DESIGN.md).

## Which repository supplies the list?

Use the active data workspace, not the recipe source repository or the directory
from which `c2j submit` happened to run. Read exactly
`<restored-workspace-root>/.c2/config.yaml` before the operation body executes.
Do not search ancestor directories on the worker, run project auto-detection,
resolve config commands, or recursively load dependent cells' configurations.
The worker needs a pure package-field decoder, not `LoadProjectConfig` followed
by project/cell resolution.

The source is the task's incoming workspace snapshot, including changes
persisted by earlier operations. That gives the following behavior:

- A root task uses its root workspace's config. Submission from another checkout
  does not inject the submitter's packages into it. Unsubmitted local changes do
  not affect a remote job; changes included by an existing snapshot submission
  mechanism do.
- Switching to cell B uses B's packages and replaces A's repository scope.
  Returning to A restores A's scope. Existing recipe/node scopes still follow
  their normal inheritance rules.
- An include changes recipe code, not the repository package source. An
  extension's source repository contributes its manifest dependencies only.
- Child jobs derive packages from their own incoming workspace, including an
  inherited synthetic snapshot where existing child semantics provide one.
  Do not copy the parent's prepared paths or repository package list into a
  child targeting another workspace.
- An empty workspace or a workspace without this file has no repository
  packages. A config created or edited by one operation takes effect on the
  next operation receiving that persisted snapshot. It cannot alter an already
  running operation's environment.
- Reading a different directory through an op input does not switch package
  scope. Use the existing workspace declaration to change the active repository.

Read the config as repository data. Require `.c2` to be a real directory and
`config.yaml` to be a regular file within the restored workspace; reject
symlinks instead of following worker-local paths. Absence is an empty list;
malformed YAML, unreadable files, or invalid packages fail preparation.

This deliberately selects per-task snapshot semantics instead of freezing the
list at job start. It supports recipes that update their own tool requirements
and follows the workspace model already used for source files. A retry restores
the same incoming snapshot, so failed-attempt edits cannot change its packages.
Record source provenance with setup diagnostics; do not independently fetch the
latest branch tip to obtain configuration.

## Scope and PATH behavior

Prepend one scope, identified as `repository:<workspace-scope-id>`, to the
existing ordered tool scopes. Use a stable root identity for implicit root
workspaces. The lookup precedence, highest first, becomes:

```text
op dependencies
nearest node, then enclosing nodes
job overrides
recipe
active repository
base process PATH
```

All declared packages must prepare successfully. A nearer declaration chooses
the default executable; it does not erase a repository requirement or uninstall
its version. An empty recipe/node list cannot clear repository packages, and
changing the execution image does not clear them either.

Reuse the existing same-scope collision behavior: an ambiguous unqualified
command fails and asks for a qualified invocation. For example, two repository
packages that export `ruff` require `uvx --from 'ruff==0.11.2' ruff ...` to select
that version, unless a nearer scope supplies an unambiguous default. Retain the
existing pnpm and Nix qualified forms as well. Do not flatten all declarations
into one scope or choose a version based on list order.

Nix already supports exposing executables through `PATH`: `nix shell` runs a
command with the selected packages on its path. However, `nix build` realizes
outputs and creates result links; it does not activate those outputs in the
caller's environment. See the official [nix shell reference](https://nix.dev/manual/nix/2.21/command-ref/new-cli/nix3-shell)
and [nix build reference](https://nix.dev/manual/nix/2.28/command-ref/new-cli/nix3-build.html).

c2j already handles activation itself: the Nix adapter retains a GC-rooted
output, discovers its `result/bin` executables, and creates invocation-local
bindings. pnpm uses an isolated project's `node_modules/.bin`; uv uses its
isolated tool bin directory. Reuse these adapters instead of nesting every
operation inside `nix shell` or mutating a shared Nix profile.

The worker base still supplies Nix, pnpm with Node, uv with Python, and Git.
Declared tools cannot bootstrap the managers needed to install themselves.
Installation uses the base manager environment, not the partially constructed
repository environment. Nix package realization continues to allow only existing
outputs or binary-cache substitutes, with local and remote package source builds
disabled. Environment capture needs the separate handling described below.

## Native libraries and environment activation

Nix dependencies include libraries as well as executables. For example:

```yaml
packages:
  - nix:nixpkgs#stdenv.cc
  - nix:nixpkgs#pkg-config
  - nix:nixpkgs#openssl
  - nix:nixpkgs#zlib
```

The intended contract is that an operation can discover these libraries with
`pkg-config`, compile against their headers, link, and run the resulting program
inside the prepared environment. Use a common pinned nixpkgs revision for the
toolchain and libraries in production. No user-authored shell activation should
be needed for this example. Cross-compilation is outside this initial contract.

The Nix installer now accepts packages without a `bin` directory and selects
outputs from derivation metadata before realization. Retain and validate the selected
development/runtime outputs and their closures, with executable bindings added
when present. Nixpkgs build-input handling selects development outputs and
propagated outputs; use it rather than guessing that headers and libraries live
beside `result/bin`. See [multiple-output packages](https://nixos.org/manual/nixpkgs/stable/#chap-multiple-output).

Prepare one native build environment for the applicable Nix dependencies using
Nixpkgs' standard environment and setup hooks. A generated `mkShell` with those
packages as inputs provides the model: activate compiler wrappers, library and
header discovery, and `pkg-config` metadata together. In particular, the CC
wrapper consumes `NIX_CFLAGS_COMPILE`, and the `pkg-config` setup hook supplies
`PKG_CONFIG_PATH`. See the [Nixpkgs setup-hook documentation](https://nixos.org/manual/nixpkgs/stable/#ssec-setup-hooks).
Simply adding every `lib` directory to `LD_LIBRARY_PATH` is insufficient.

Use a pinned, c2j-supported nixpkgs standard environment for the generated shell,
record its revision as part of the environment identity, and select each
package's default, development, runtime, and executable outputs while its
derivation metadata is available. The selected development store paths can then
be passed to the shell without losing output selection. Their retained
`nix-support` files supply propagated-input metadata.
Changing the activation runtime invalidates its cached environments.

The [prototype](prototypes/nix-native-environment/README.md) confirms this
activation path on `aarch64-linux`, using Nix 2.35.1 and a pinned Nixpkgs revision:

1. Evaluate the generated consumer `mkShell` and inspect its derivation. Keep
   import-from-derivation disabled. The derivation identifies its input
   derivations and exact required outputs, including development outputs.
2. Realize only those inputs with `--max-jobs 0 --builders ''`, retaining output
   links/GC roots. Do not realize the generated shell itself.
3. Start the realized Bash builder in a clean environment populated from the
   generated derivation, and source the realized `stdenv/setup`. Capture exported
   values using `env -0`, without running build phases or user `shellHook` code.
4. Retain the supported activation variables and discard capture-directory and
   build-only state. Subsequent operations use that saved environment directly.

Set `NIX_NO_SELF_RPATH=1` during capture: otherwise setup adds the temporary
output directory to `NIX_LDFLAGS`. Use `NIX_ENFORCE_PURITY=0` for ordinary
development builds against workspace files. This is a compiler-wrapper setting,
not permission for Nix to build package dependencies. Delete the temporary
capture directory before checking that the environment is usable.

The prototype also confirms that `nix print-dev-env --json` cannot simply replace
this capture step: under `--max-jobs 0` it fails because its capture derivation
needs building. Directly activating the already-realized inputs avoids that
helper and requires **no exception** to the source-build restriction. The
prototype rejects non-substitutable inputs with both values of
`preferLocalBuild`, even when ambient configuration enables local/remote builds.

The recorded tests compile and run a C program using OpenSSL and zlib, both with
`pkg-config` flags and with only `-lcrypto -lz`. The binary runs without any
activation variables or `LD_LIBRARY_PATH`; saved activation also supports fresh
compilation without Nix on PATH and after garbage collection. See the
[recorded results](prototypes/nix-native-environment/result.json).

Production integration defines the activation-variable contract and supports
legacy and version-4 derivation JSON. It rejects structured attributes and
dynamic outputs explicitly, detects duplicate pkg-config providers (including
propagated search paths) and multiple declared compiler wrappers, and includes
real task-worker coverage. Additional platforms/toolchains still need validation.

Extend `toolenv.Environment` from a PATH-only result to retained package outputs
plus an invocation-local environment delta. Capture in a clean setup process;
retain exported activation variables, not shell functions, build-phase state,
temporary build paths, `HOME`, or setup-process credentials. Apply that delta
through a process-context helper shared by commands and extension ops. Explicit
op environment values override scalar activation defaults; c2j's executable
bindings remain first on `PATH`, followed by the activated Nix tool paths and
the op/base path. Never mutate the worker's global environment. Warm execution
uses the retained activation result without invoking Nix again.

Readiness checks validate retained outputs and the activation result, including
libraries with zero executables. Cache the composed environment by ordered
scopes, resolved output identities, platform, and activation-runtime version.
Retain GC roots for all referenced outputs, including development outputs, not
only the original default output. Bump the cache format for this richer record.

Executable precedence stays as documented above. Library compatibility needs
different treatment: a qualified executable runner cannot isolate headers or
ABIs. Compose one coherent native environment per invocation. Detect conflicting
providers of the same `pkg-config` module or incompatible toolchains and fail
preparation rather than choosing by incidental path order; isolate such demands
in different invocations. Arbitrary header/ABI conflicts cannot all be detected
upfront, so successful preparation is not proof of compatibility. Prefer one
consistent Nix package set and test compilation and runtime behavior.

For runtime linking, use the selected Nix toolchain's linker/runtime-path
behavior and package wrappers. Do not inject a blanket `LD_LIBRARY_PATH` into
every process. Software loading plugins by bare name may need an explicit
loader path or package wrapper. An arbitrary prebuilt binary expecting a
different libc, loader, or ABI is not made compatible by declaring a library;
that case may require a suitable image or Nix packaging.

## Other package availability boundaries

The `pnpm:` and `uv:` adapters continue to install isolated applications; this
native-library extension does not automatically add JavaScript or Python
imports to the project's environment. Project `node_modules`, Python virtual
environments, and Go module dependencies remain managed by ordinary recipe
steps and the project's own lockfiles. Broader language-library activation can
extend those adapters separately without restricting Nix's native libraries.

`pnpm:playwright@...` makes the Playwright CLI available. Running browser tests
also requires browser binaries matching that Playwright version and OS
dependencies; installing the CLI alone does not guarantee a working browser.
Playwright documents those as separate installation steps in its
[browser setup guide](https://playwright.dev/docs/browsers).
Supply them in the execution image or through explicit recipe preparation
appropriate to that image. A Nix package wrapping a complete browser runtime is
also possible if it is already available from the configured binary cache.
This proposal adds neither implicit `playwright install --with-deps` nor generic
post-install shell hooks. Browser provisioning and project library activation
remain explicit limitations of this initial design for pnpm/uv packages.

## Live execution, replay, and failure handling

The task-result cache remains the outer boundary:

```text
task-result hit → replay recorded outcome; no config read or package setup
task-result miss
  → resolve extension metadata and validate/authorize inputs
  → restore incoming workspace using the base worker tools
  → read and validate repository packages
  → combine scoped declarations and prepare tools
  → activate the prepared environment and invoke operation
  → persist workspace/result and setup diagnostics
```

Do not install packages at submission, recipe compilation, workspace selector
resolution, or entry into an unvisited scope. Existing config inspection may
validate local syntax without installing anything. On worker handoff, the next
live operation restores its input snapshot and reuses or prepares its tools on
the new executor. Completed tasks remain completed even if installations have
been evicted. Concurrent preparation reuses the existing tool-cache locks.

Today, [taskworker.go](pkg/worker/ops/taskworker.go) calls `InstallInvocation`
before [op_executor.go](pkg/worker/ops/op_executor.go) restores the workspace.
Reading repository config in that earlier phase would use the wrong filesystem
or require a duplicate checkout. Refactor the shared live invocation path to
provide a preparation hook immediately after `controller.Restore` and before
invoking the op body. Keep extension resolution and input authorization before
installation; pass their result into this hook. Perform one combined
`Manager.Prepare` call after inserting the repository scope.

Pass preparation state by reference or return it explicitly to the task wrapper;
`opExecutor` currently receives the invocation by value. The outer wrapper must
retain setup results even when config parsing or installation fails. Update the
failure-diagnostic condition, which currently considers only the original
preparation scopes/extension, so repository-only preparation failures produce
`_c2j/preparation.json` too. Bind the resulting tool environment only for the op
body and its child processes; Git restore/persistence infrastructure continues
to use the base tools.

Preparation still shares the task timeout and honors cancellation and enclosing
deadlines. Include config discovery and tool setup in `preparation_ms`, retaining
the existing `setup.wall_ms` and per-tool timings. Account for workspace restore
consistently after the lifecycle refactor and document its inclusion in the
overall preparation duration. A config or package setup failure prevents the
op body from running. A worker crash may prevent diagnostics from being emitted,
as with current preparation failures.

Extend setup diagnostics with repository provenance: workspace scope, repository
identity, incoming snapshot identity, config path, digest of config bytes, and
the normalized package list. Mark a missing file explicitly. Per-tool records
retain reference, installation identity, scope, timing, and outcome. Do not
store the entire project config or host-specific paths as portable requirements.
The config digest is provenance, not a claim that package versions are locked.

Replay/retry identity must continue to include the incoming workspace snapshot
and restore-artifact identity. Verify this during implementation rather than
introducing a second package-resolution task or reading live files while
constructing cache keys. Config changes take effect through a changed workspace
input; a cached result never re-reads config. Since this changes task behavior,
version the affected activity contract/cache identity so pre-feature results
cannot silently stand in for execution with repository requirements.

## Implementation and verification

The implementation extends the existing package system:

- `pkg/config/packages.go` decodes the shared package reference format, provides
  the immutable config accessor, and loads the exact workspace's file without
  project discovery or command evaluation. `c2j self` and init templates expose
  the new field.
- `pkg/toolenv/native.go` retains selected outputs and native activation, using
  a pinned standard environment and substitution-only input realization.
  `pkg/ops/process` applies invocation-local variables without global mutation.
- `pkg/worker/ops` prepares once after restore, retaining input authorization,
  cancellation, provenance, and failure artifacts. The compiler's activity
  contract is versioned to invalidate pre-feature results.
- `pkg/recipetest` uses the same loader/preparation helper for passthrough;
  fully mocked tests bypass it.

Automated coverage includes parsing all managers, portable-reference validation,
config provenance and bounded lookup, environment precedence/isolation, failure
artifacts, workspace switches, a child sourced from another repository, config
edits between tasks, empty workspaces, node precedence, and completed replay
without repositories or installation caches. Existing shared-adapter tests
continue to cover executable ambiguity and qualified invocation.

Real Nix container tests compile and run against OpenSSL and zlib, including
libraries without executables, implicit and explicitly declared compilers,
retained development outputs, garbage collection, deleted-root repair, and warm
activation with no managers on PATH. A real worker/recipe test loads dependencies
only from a committed `.c2/config.yaml` and runs a C compilation command.
Source-build rejection remains covered by the real Nix installer test.

The native runtime has been validated with Nix 2.35.1 on Linux ARM64. Additional
platforms, cross-compilation, arbitrary binary ABI compatibility, and project
language import environments are outside this validation. Upgrade compilers and
workers together; older binaries may ignore the unknown config field and cannot
enforce its dependencies.

The implementation adds a declaration scope and native-library
activation to the existing package system. Project language environments,
browser provisioning, image construction,
relative filesystem package references, and a global package lock service are
separate work. For repository declarations, initially require portable registry
or remote Nix references: reject local paths, `file:`/`link:`/`workspace:` specs,
and local flake selectors such as `nix:.#tool` with an actionable error. The
current installer runs in a cache directory and its key does not include source
contents, so treating those as repository-relative would otherwise install the
wrong source or reuse stale results. Apply this additional location check only
to the new field; do not silently change existing recipe/op parsing semantics.
