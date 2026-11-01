# Portable execution model

`Demand` version 3 is a scheduling hint containing resolution status and effective
requirements. It is stored in the client-owned `c2j.execution` payload namespace.
`Submission` adds the original job overrides and is saved in metadata and start
input. `Inspect` selects the latest scheduling snapshot over the submission hint
and labels unresolved, specified, unspecified, malformed, and unsupported state.

Every invocation reconstructs requirements from the submitted overrides, recipe,
and retained activity results. The payload is never used as replay state. It
contains no recipe digest, node overlay, directive checkpoints, or allocation.
Changes publish with a JobDB reschedule; compatible work proceeds immediately.
Restart initializes its hint from the original submission metadata. JobDB's
client-payload revision handles concurrency; there is no separate demand revision.

This package implements the shared value model for portable execution
requirements. It does not provision environments, change JobDB lease matching,
or enable recipe execution handoff by itself. The compiler and
[`executionruntime`](../executionruntime/README.md) wire this model to JobDB's
independent client payload and explicit workflow yield. See the
[upgrade notes](../../JOBDB_UPGRADE_NOTES.md) for the current client-payload and
typed-route contract, and the
[design](../../C2J_PORTABLE_EXECUTION_REQUIREMENTS_DESIGN.md) for feature intent.

`Requirements` uses optional string pointers for image, platform, CPU, memory,
and ephemeral storage. `Normalize` validates and canonicalizes supplied values.
`Overlay(base, patch)` copies fields, with supplied patch fields replacing the
base, including lower resource minima. Normalize each input before overlaying:
empty, zero, and negative values are errors, not requests to clear a field.

CPU accepts cores or millicores at integral millicore precision. Memory/storage
accept SI (`k`, `M`, `G`, `T`, `P`, `E`) or IEC (`Ki` through `Ei`) units and must
resolve to integral bytes. Scalars must fit signed 64-bit integers. Kubernetes
quantity parsing is checked for rounding and overflow; quantities are never
silently rounded down or capped. Canonical byte values always retain a unit
(one byte is `0.001k`). Platforms use `os/architecture[/variant]`, normalized to
lowercase; a requirement without a variant accepts any variant of that same
OS/architecture.

`Allocation` version 1 describes actual simultaneous usable capacity, injected
by a trusted provisioner. It must not be inferred from recipe requests, host
capacity, or a previous attempt. Its image has three independent fields:

- `reference`: normalized launch reference/tag.
- `manifest_digest`: actual resolved OCI manifest digest.
- `image_id`: diagnostic runtime/config identity; never used as a manifest digest.

`Compare(requirements, allocation)` validates both operands and returns
structured mismatches. Unknown allocation fields cannot satisfy explicit
requirements. Resources use minimum-capacity comparisons; image tags match
launch references, and pinned images require the actual manifest digest.
A pinned launch reference alone is not proof of the actual manifest. Empty
requirements are compatible with unknown allocation. Compatibility is not a
lease, resource reservation, or readiness guarantee.

Run package tests with `go test ./pkg/execution`.

## Execution capabilities

`Requirements.Capabilities` and `Allocation.Capabilities` are optional string
sets. Requirements compose by union across recipe, enclosing/current node, and
job declarations. Submission patches and `ops.SuspendExecution` add requirements;
omission or an empty list adds nothing. A wholly empty runtime patch still fails
existing validation. Node contributions disappear when their scope ends; durable
job directives survive replay. Child jobs retain their independent requirements.

Names match `[a-z][a-z0-9]*(?:[._/-][a-z0-9]+)*` exactly, with no case folding,
wildcards, hierarchy, implication, or version expressions. Unfamiliar valid names
are accepted. Normalization deduplicates and sorts names. Matching requires every
requested name to be supplied and permits extra allocation facts. Each missing
name produces a `capabilities` mismatch with reason `missing_capability`.
An omitted allocation set supplies no capabilities.

```yaml
execution:
  capabilities: [docker, docker.workspace-mounts]
sequence:
  - id: virtual-machine
    execution_needs:
      capabilities: [virtualization.kvm]
    op: command_execution
    inputs:
      run: ./run-guest-tests
```

Capabilities describe usable interfaces, supplied by the provider to the task
identity. c2j neither provisions nor probes them. Initial contracts are:

| Name | Provider contract |
| --- | --- |
| `docker` | Configured Docker Engine API access to pull images and create, start, inspect, stop, and remove containers; cleanup of job-owned resources. |
| `docker.workspace-mounts` | Bind mounts accept task-visible workspace paths, with documented ownership and access behavior. |
| `docker.privileged-containers` | The Docker service permits privileged child containers. |
| `virtualization.kvm` | Usable KVM API access and permissions to create and run guests. |

A provider can expose Docker through a daemon in a VM, a configured container,
or an attached service. It reports `docker.workspace-mounts` separately only when
workspace paths work with that daemon. KVM can be supplied on a physical host or
in a VM with working nested virtualization and task device permissions. Providers
must document platforms and limitations and establish readiness before reporting
facts. A CLI package or device-file presence alone establishes no capability.
Client software remains an image/package responsibility. `docker` alone promises
neither workspace mounts, published-port reachability, privileged containers, nor
image-building extensions. Provider policy, accounting, isolation, and cleanup
remain provider responsibilities; requests do not authorize privileged setup.

All allocation-aware CLI paths (run, supplied lease, embedded workers, and opt-in
list filtering) accept repeated `--execution-capability NAME` or
`C2J_EXECUTION_CAPABILITIES='["docker","docker.workspace-mounts"]'`. Explicit flags
replace the entire environment set. Submit with repeated `--require-capability
NAME`. Requested capabilities never supply allocation facts; every execution
attempt receives its own trusted allocation. Public Go callers use the same
`Requirements` and `Allocation` fields.

Capability-only constraints require an execution-aware runtime and guarded task
workers. Missing facts yield `environment_required` before dependent live work.
Cached work needs no historical allocation, while cached directives still affect
later live work. Demand snapshots, listing JSON, and handoff diagnostics carry the
same effective set.

Schema numbers remain unchanged: demand/submission **3**, allocation **1**.
All producers and consumers must upgrade together. Existing records that omit
capabilities retain their behavior and need no data migration; new readers treat
omission as an empty set. Old binaries reading new records and mixed-version
rollouts are unsupported.
