# Pulse: reusing prepared tool environments

Design discussion, not an implementation. Extends the
[package proposal](PROPOSAL_EXECUTION_ENVIRONMENT_ADDONS.md) for pnpm, Nix, and uv.

## Reuse installed tools, not just downloads

The reusable unit is a **prepared tool environment**: installed packages, runtime
dependencies, and activation settings. Its representation is provider-specific;
an image is one possible representation, not the starting assumption.

Check the **task-result cache first**. A hit replays the result without resolving
dependencies or contacting the tool preparer. Only a live task requests its
applicable scope and op tools; the provider's package/environment cache then
determines whether setup requires installation or just attachment. Do not prepare
tools for unvisited branches or scopes containing only cached tasks.

Distinguish three kinds of reuse:

| Reused state | Work still needed |
| --- | --- |
| Download caches | Resolve/install/link packages; possibly run build scripts |
| Installed packages and runtimes | Assemble their environment and expose executables |
| Complete prepared environment | Attach it and supply version-specific runner bindings/PATH; no package installation |

For repeated jobs needing the same six tools, target the third case. Container
startup, scheduling, and recipe resolution still take time; near-zero additional
tool provisioning is the goal, not a guaranteed end-to-end launch latency.

## Candidate local implementation: persistent store plus directories

1. Keep a provider-owned Nix store across invocations. The base supplies Nix,
   pnpm/Node, uv/Python, and git; first preparation installs declared packages.
2. Prepare the version-specific environments used by `nix run`, `uvx --from`,
   and `pnpm --package ... dlx`. Retain completed installations at stable
   container-visible paths; different versions can coexist. This replaces the
   earlier assumption of one global tool installation per environment.
3. Publish the completed environment only after validation. Subsequent containers
   mount the required store paths and prepared directories read-only and receive
   the recorded invocation-specific runner bindings/PATH. Their homes/workspaces
   and any runner bookkeeping that requires writes remain separate and writable.

Nix already separates installed package versions and exposes them through
profiles, so different environments can reference the same installed packages.
Retain the necessary closures with GC roots while environments are in use.
[Nix profiles](https://nix.dev/manual/nix/2.28/package-management/profiles)

The preparer owns store writes; workload containers should not share a writable
global profile or tool installation. Mount `/nix/store` at its expected path and
preserve every profile/symlink target required at runtime. Mounting over the
base's store must not hide dependencies of the base itself. This store/base
composition needs a prototype, not an assumption that an arbitrary host `/nix`
directory can be attached unchanged.

Preserve Python interpreter paths and pnpm executable/link targets. Native runner
caches may be disposable, so provider retention must keep prepared environments
available while needed. Keep installed tools distinct from download-only caches.
Use stable paths during installation and execution to avoid relocating virtual
environments. [uv tools](https://docs.astral.sh/uv/concepts/tools/)

Preparation finishes before the timed op task is scheduled. Recipe/sequence/node
tools are resolved and prepared at the first live task in their scope; concurrent
live tasks share that barrier. Additional op dependencies are resolved and prepared
just before that op's live invocation. Prevent runner cache expiry or refresh
from triggering installation inside the timed task; use the prepared executable
directly when needed. Report
setup duration and per-tool reuse/preparation outcomes with task diagnostics,
including resolution time and referencing shared scope setup once. Cached replay
creates no setup attempt. See the main proposal for accounting rules.

## Provider boundary

Common contract: **prepare this base/platform/package set → return a ready
environment handle and activation information**. The handle is provider-owned;
it might describe mounts, an artifact, or an image. It becomes allocation evidence
only after the provider attaches the environment successfully. Provider fallback
must prepare or locate its own compatible environment. The handle includes
scoped version selection: storing two versions does not choose which a plain
command name invokes.

This contract is demand-driven, with no upfront full-recipe preparation phase.
A minimal c2j runner can traverse/replay first and request preparation outside the
workload container when it reaches live work.

| Provider | Mechanisms to evaluate |
| --- | --- |
| Local Docker / persistent VM runner | Provider-owned disk, Nix store and prepared directories mounted into new containers; snapshots or image layers are alternatives. |
| Remote runner service | Runner-local storage managed by that service; routing to a warm runner may improve reuse. |
| Cloud Run Jobs | No assumption of a persistent host-local store. Compare an NFS-backed prepared environment, restoring a prepared artifact, and a prebuilt image. |

Cloud Run Jobs supports NFS mounts, but mounting and reading tools across the
network has its own latency and infrastructure cost. Restoring an archive avoids
installation but still transfers/unpacks files. Image delivery may prove better
there; measure rather than prescribe it for every provider.
[Cloud Run NFS](https://docs.cloud.google.com/run/docs/configuring/jobs/nfs-volume-mounts)

Pulse's current Docker provider inspects local images and pulls missing ones;
its reviewed launch path does not prepare package environments. Directory mounts
and preparation would be additions, not existing cache functionality.
[Reviewed source](https://github.com/colony-2/pulse/blob/42187038298a00302a3d2a903fca5becb08497aa/internal/providers/docker/docker.go)

## Minimal reuse rules

- Identify an environment by normalized package set, target platform, compatible
  base identity, Nixpkgs/runtime selections, and preparation implementation/settings.
  Exclude job IDs, workspace content, CPU, and memory from tool identity.
- Coalesce concurrent requests, publish only completed installations, and retain
  environments while containers use them. Namespace reuse by access boundary
  when private packages are involved.
- Mutable package/base references need an explicit refresh policy. Reusing a
  prepared environment means reusing its installed versions, not silently checking
  for updates on every invocation. A refresh creates a new completed environment.
- Losing a tool cache triggers preparation at the next dependent live task;
  cached task results still replay without it. Providers own
  retention/eviction; c2j needs readiness facts, not their storage layout.

## Where layers fit

OCI layers can reuse installation output when the provider chooses an image
strategy. BuildKit caches can also accelerate changed environments, but ordinary
layers depend on preceding layers; they are not freely interchangeable tool
bundles. Do not invent a cross-provider layer system.
[Docker build cache](https://docs.docker.com/build/cache/),
[cache invalidation](https://docs.docker.com/build/cache/invalidation/)

First experiment: a Nix-enabled base, two pnpm tools, two uv tools, and two Nix
tools. Compare fresh preparation, repeated attachment, and a one-tool change.
Measure provisioning separately from container startup, and verify task-user
access, simultaneous jobs, and store retention before choosing the local backend.
