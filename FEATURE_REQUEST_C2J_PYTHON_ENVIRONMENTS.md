# Provide a working Python environment for task libraries

Owner: c2j. Type: enhancement, with a related preparation bug investigation.

Implementation status: the c2j declaration, uv preparation/reuse, interpreter
binding and diagnostics are implemented. See [execution tools](EXECUTION_TOOLS.md#python-script-environments)
for the supported contract. Local real-uv tests include source-build reuse and a
Pillow PNG round-trip. Published base2/Pulse validation on both architectures and
the unchanged adaptive3 validator remain rollout acceptance work.

Related goal: `3M2nxdy26CqBg7b2FzRob6uoeMN`, job
`3M2nxf25SYsee0HnOxsaw94fLpG`. This request expands the
[existing Pillow report](BUG_REPORT_C2J_PYTHON_DEPENDENCY_ENVIRONMENT.md);
it has not been submitted as another job.

## Problem

A recipe should be able to request Python plus libraries, then run a script that
imports them. Today, requesting packages does not establish that relationship.

Our review validator declares Nix `python3`, `python3Packages.pillow`, and `cmark`
at one pinned nixpkgs revision. c2j reports successful preparation, but the task
fails at `from PIL import Image`. Pillow is the package name; `PIL` is its import
name. The same failure prevents reviewing proposals to repair the environment.
The [real-worker reproduction](adaptive3/VALIDATOR_WORKER_CHECK.md) runs the
unchanged validator in two separate tasks; both imports failed.

c2j main was checked at `3fec5e455133216ab103a4eb693143e09640742b` on
2026-11-13:

- [`EXECUTION_TOOLS.md`](https://github.com/colony-2/c2j/blob/3fec5e455133216ab103a4eb693143e09640742b/EXECUTION_TOOLS.md)
  explicitly describes `uv:` as isolated CLI installation, not Python library
  installation into a script's environment.
- [`environment.go`](https://github.com/colony-2/c2j/blob/3fec5e455133216ab103a4eb693143e09640742b/pkg/toolenv/environment.go)
  uses `uv tool install` and expects exported executables.
- [`native.go`](https://github.com/colony-2/c2j/blob/3fec5e455133216ab103a4eb693143e09640742b/pkg/toolenv/native.go)
  captures selected Nix setup variables but excludes `PYTHONPATH` and
  `PYTHONNOUSERSITE`. It does not establish a Python interpreter/library pairing.

The preparation failure is real. Whether the existing Nix declaration should
work under c2j's current contract remains part of the bug investigation. We should
not describe changing `uv tool install` as a bug fix: it implements its documented
CLI-tool contract.

## Recommendation

Add a Python block using standard Python metadata, backed by uv. c2j reuses a
prepared environment when available. On a cache miss, it asks uv to select a
compatible permitted interpreter, resolve dependencies and prepare the environment.
The task uses that environment's `python` and `python3`. An isolated environment
is a dependency combination, not a new installation for each task. This belongs
in existing task preparation, with no extra recipe task or environment-install
script maintained by every recipe.

uv supports creating virtual environments and installing packages into a specified
interpreter's environment. Its tool installer instead exposes applications from
separate environments. Simply declaring `uv:Pillow` would not make Pillow
importable by an unrelated `python3`.
([uv environments](https://docs.astral.sh/uv/pip/environments/),
[uv tools](https://docs.astral.sh/uv/concepts/tools/))

Nix remains an alternative for separately declared prebuilt environments:
`python.withPackages` composes an interpreter with its matching library set.
Preserve the existing prohibition on building missing Nix outputs during task
preparation. This Python block uses Python dependency semantics and uv; it does
not require a second backend, automatic translation into nixpkgs attributes, or
replacing Nix elsewhere.
([Nixpkgs Python environments](https://github.com/NixOS/nixpkgs/blob/master/doc/languages-frameworks/python.section.md))

## Share installed environments across executions

This is a primary requirement. Reuse installed software, not just downloaded
archives. Different cells requesting the same Python and dependencies should
select the same ready environment. A warm execution should need only local
readiness checks and interpreter/PATH binding: no uv invocation, resolution,
download or installation.

Pulse already provides the storage for this. At revision
`a3aa1c185d20c107edbfda0f702cd3178bf57aae`, its Docker provider mounts a shared
tools volume at `/var/cache/pulse` and sets:

```text
C2J_TOOL_CACHE_DIR=/var/cache/pulse/images/<image-key>/tools
UV_CACHE_DIR=/var/cache/pulse/images/<image-key>/uv
```

Both locations are on the same filesystem. Pulse scopes the volume by JobDB
instance, tenant, platform, namespace and generation. Prepared environments and
uv caches additionally use separate directories for each base image. The shared
Nix store spans those images. These are local Docker volumes, not a distributed
cache across hosts. Pulse also sets `UV_PYTHON_DOWNLOADS=never`; use the supplied
compatible interpreter or fail preparation clearly rather than downloading one.
([Pulse cache configuration](https://github.com/colony-2/pulse/blob/a3aa1c185d20c107edbfda0f702cd3178bf57aae/docs/configuration.md#docker-dependency-caches),
[environment bindings](https://github.com/colony-2/pulse/blob/a3aa1c185d20c107edbfda0f702cd3178bf57aae/internal/providers/docker/cache.go))

Use two existing forms of reuse:

| Request | Expected work |
| --- | --- |
| Cell A and cell B need the same complete Python environment | Reuse the ready environment under `C2J_TOOL_CACHE_DIR`; bind its interpreter. |
| Cell B needs the same Pillow build plus another library | Prepare a distinct complete environment once. Reuse Pillow's unpacked files from `UV_CACHE_DIR` using hardlinks or copy-on-write clones; only missing dependencies need fetching/preparation. |
| A later task needs either environment | Reuse it directly, including after a container or Pulse restart. |
| A task runs under another image, incompatible interpreter/platform, or host without the shared volume | Prepare a separate compatible environment using the caches available there. Do not claim zero-work sharing across this boundary. |

uv already implements hardlinks and copy-on-write clones when installing cached
packages. Use its existing `link-mode` support; c2j should not implement file
deduplication or package linking itself. Keep the cache and environments on the
same filesystem. Prefer those modes over symlinks into the disposable uv cache:
cleaning that cache must not break a completed environment.
([uv caching](https://docs.astral.sh/uv/concepts/cache/),
[link modes](https://docs.astral.sh/uv/reference/settings/#link-mode))

Extend c2j's existing prepared-tool cache and locking. The lookup must not depend
on cell, job, task or worktree identity. Store the resolved dependency set with the
ready entry so a warm lookup does not need another resolution. Construct each
environment at its final stable absolute path under a lock and mark it ready only
after preparation succeeds; virtualenv launchers can contain absolute paths.
Treat ready environments as immutable. A different dependency set gets another
entry, rather than modifying an environment another execution is using.

Pulse owns volume mounts and retention; c2j owns environment identity, preparation
and binding. Do not add a new shared-storage service or an environment-layering
system. Cross-image or cross-host sharing beyond Pulse's current boundary would
need a separate provider change; it is not implied by choosing uv.

## base2 and rollout

The base repository at `3a7d9c10d99e2f42669ffde24fa5ff45cb6fe1f1` already
installs uv, Python, virtualenv support and c2j. Its Dockerfile does not force
`UV_LINK_MODE=copy`, and its smoke test checks that uv can create a working
virtualenv with the supplied Python. This is source inspection, not verification
of the image currently deployed to every worker.
([Dockerfile](https://github.com/colony-2/base/blob/3a7d9c10d99e2f42669ffde24fa5ff45cb6fe1f1/Dockerfile),
[Python packages](https://github.com/colony-2/base/blob/3a7d9c10d99e2f42669ffde24fa5ff45cb6fe1f1/apt/packages.txt),
[smoke test](https://github.com/colony-2/base/blob/3a7d9c10d99e2f42669ffde24fa5ff45cb6fe1f1/scripts/smoke-test.sh))

No base2 configuration change is expected just to enable linking. If an explicit
mode is needed, c2j can pass `--link-mode` for its own preparation commands.
Validate the chosen mode on the actual shared-volume filesystem; avoid changing
every uv invocation in the image merely to configure this feature.

| Owner | Delivery work |
| --- | --- |
| c2j | Implement environment preparation/reuse and interpreter binding using uv's existing installation/linking facilities. Publish a version containing the feature. |
| base | Rebuild and publish base2 with that c2j version. Verify the installed Python and uv support the feature on both published architectures. Change their versions only if testing establishes a need. |
| Pulse | Use its existing mounts and cache variables. Update its c2j Go-library dependency if needed to accept the new execution requirement. Verify reuse across separate containers and controller restart; no package-management logic or mount redesign is expected. |
| recipes | Adopt the supported declaration and run the unchanged validator and package-probe flow through real preparation. |

Before declaring rollout complete, run the acceptance tests below with the
published base2 image under Pulse, recording its digest and actual c2j/uv/Python
versions. The current virtualenv smoke test is useful but does not prove Pillow
installation, interpreter binding or shared-volume reuse. Keep that distinction
in the release result.

## Recipe shape

Supported syntax; version choices illustrate the shape rather than prescribe a
release. Select versions supported by the
deployed image; the example does not authorize automatic interpreter downloads:

```yaml
execution_needs:
  packages:
    - nix:github:NixOS/nixpkgs/<approved-commit>#cmark
  python:
    requires-python: ">=3.13,<3.14"
    dependencies:
      - "Pillow==12.0.0"
inputs:
  run: |
    python3 - <<'PY'
    from io import BytesIO
    from PIL import Image
    data = BytesIO()
    Image.new("RGB", (2, 2)).save(data, format="PNG")
    data.seek(0)
    with Image.open(data) as image:
        image.load()
        assert image.size == (2, 2)
    PY
```

Use the standard names and meanings from `pyproject.toml` and Python's inline
script metadata:

- `requires-python`: an optional string specifying compatible Python versions,
  using standard Python version specifiers. This is a compatibility constraint,
  not permission to download another interpreter.
- `dependencies`: a list of standard Python dependency strings. Preserve support
  for version constraints, extras and environment markers; use `[]` when no
  libraries are needed. No `uv:` or `python:` prefix appears inside this list.

This is a YAML representation of those fields, not a new Python metadata
standard. uv already consumes the corresponding standard metadata and supports
dependency resolution and locking.
([Python metadata specification](https://packaging.python.org/en/latest/specifications/inline-script-metadata/),
[uv script dependencies](https://docs.astral.sh/uv/guides/scripts/),
[uv resolution and locking](https://docs.astral.sh/uv/pip/compile/))

Use this declaration at the existing repository, recipe, node and operation scopes
where appropriate. A nearer block selects a complete environment rather than
merging Python dependency sets across scopes. Keep `uv:` CLI declarations working
as they do today. No backend selector or broader copy of uv's configuration schema
is needed for the first version.

## Delegate Python semantics to uv

c2j should understand the block's shape and its place in task execution. It should
not become a Python package manager.

| Work | Owner |
| --- | --- |
| Check supported keys, a string `requires-python`, and a string-list `dependencies`; apply recipe scope rules | c2j |
| Parse Python version/dependency expressions, extras and markers; select a compatible interpreter from permitted sources | uv |
| Resolve transitive dependencies and conflicts, select compatible distributions, create the virtualenv, install and link cached files | uv |
| Produce the resolved dependency record or lock data | uv; c2j retains it with the prepared environment |
| Apply worker download/build/authorization policy, choose stable cache locations, serialize initial preparation and bind the completed interpreter to the task | c2j, using its existing preparation infrastructure |
| Retain shared storage and its existing tenant/image boundaries | Pulse |

On a cache miss, serialize the fields into metadata accepted by a supported uv
interface and invoke uv during preparation. Preserve the dependency strings;
do not translate them into a custom dependency model or implement version
comparison, marker evaluation, a resolver, wheel selection or file linking in Go.
Use normal structured serialization and argument passing. Any generated metadata
belongs in runtime preparation storage, not the worktree or recipe artifacts.

Basic structural errors can be rejected by c2j immediately. Detailed Python
syntax, compatibility and resolution errors should come from uv and remain visible
in preparation diagnostics. On failure, the task body must not run. On a warm
hit, use the ready environment without invoking uv again. Installations must not
be deferred into the task body through an unrestricted `uv run` command.

This leaves a small runtime adapter, not merely unchecked pass-through: uv cannot
know c2j's task boundaries, worker policy or which shared environment a task should
receive. Reuse the existing cache/locking/diagnostic mechanisms for those duties.

## Required behavior

1. **Bind installation to execution.** Libraries are installed for the interpreter
   that actually runs the task and its subprocesses. The selected `python` block
   owns the `python` and `python3` bindings, including when a Nix interpreter is
   separately declared. Prevent ambient Python variables from contaminating its
   imports. Separately installed CLI tools retain their own interpreters.
2. **Prepare before execution.** Resolution, installation and compatibility failures
   stop preparation with the relevant error. Do not mark failed preparation ready
   or run the command body. Allow uv to build Python dependencies from source
   during preparation using the worker's available build tools. Reuse compatible
   built wheels through uv's cache; this is not a promise of one build across
   independent caches or incompatible images, interpreters, or platforms. Missing
   build prerequisites or build failures stop preparation. Follow existing package
   authorization and interpreter-download policy; the Nix no-build rule remains
   unchanged.
3. **Keep tasks independent.** Do not rely on a previous task's activated shell,
   modify system Python, or create a `.venv` in the managed repository. A task on
   another worker must get an environment satisfying the same declaration, reusing
   a shared ready entry when that worker has access to it. Independent cold caches
   may resolve different compatible dependency versions. Retain each cache's
   resolution for reuse; v1 does not promise identical versions across independent
   caches or introduce a portable lock.
4. **Reuse safely.** Cache by the selected interpreter identity, platform, resolved
   requirements and preparation format. Preserve resolved versions for reuse;
   direct version pins alone do not lock transitive dependencies. Never reuse a
   partial install or an incompatible environment after an activation-format change.
5. **Explain what ran.** Existing preparation diagnostics should identify the actual
   Python executable/version, requested and resolved packages, and whether its
   environment was created or reused. Keep full installer errors in command logs.
6. **Document both paths.** Explain when to use a CLI package versus a script-library
   environment, how repository defaults interact with task declarations, and how
   callers can use a composed Nix environment. Either support the original compatible
   Nix Python/library combination or document the required replacement unambiguously.

Preparation establishes the environment; it cannot certify arbitrary application
behavior or infer every import name. Recipe-specific checks still test actual use.
The existing package-probe job should test that same declared environment before
a requested configuration is adopted. Recipes owns that integration and its
release tests; c2j owns environment preparation and interpreter binding.

## Acceptance tests

Run real tasks with the published base2 image and Pulse's actual volume mounts,
starting with no preinstalled Pillow or manual import paths. Cover both supported
architectures. Do not substitute mocks for preparation:

- A fresh task imports Pillow and creates, saves and decodes a PNG using its selected Python.
- Two cells in separate Pulse containers reuse one ready environment with network access disabled on the warm run and no package-manager invocation.
- Restarting Pulse or replacing a container preserves that reuse on the same image and cache generation.
- A new environment with an extra library reuses the compatible Pillow files through links/clones without another Pillow download or build.
- Cleaning uv's download/package cache does not break already prepared environments.
- Concurrent identical requests prepare one complete environment; interrupted preparation never exposes a ready entry.
- Image/platform changes and cache-generation rotation select the correct separate environments; a host without the shared volume prepares independently.
- A conflicting ambient Python does not change the selected interpreter or its imports.
- Standard extras, version ranges and environment markers have uv's normal semantics without a separate c2j dependency evaluator.
- Wrong field types are rejected structurally; invalid Python specifiers and dependency conflicts retain uv's preparation error.
- Interpreter selection honors `requires-python` and the worker's download policy; no compatible permitted interpreter means preparation fails.
- An unavailable package, unsupported interpreter or interrupted install prevents command execution and retains the actual setup error.
- Two tasks requiring different library versions remain isolated, including concurrent preparation.
- Existing `uv:` CLI tools still work alongside the Python environment.
- The unchanged adaptive3 validator passes both Markdown and image checks through normal preparation.

Fixing the existing Nix activation bug can restore current jobs sooner. Deliver
that correction independently if possible; do not hold it behind the new API.
Moving Pillow's import into its image check reduces the text-review dependency,
but does not satisfy this environment request or the image acceptance tests.
