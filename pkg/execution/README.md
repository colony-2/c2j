# Portable execution model

`Demand` version 4 is a scheduling hint containing resolution status and effective
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

`Requirements` uses optional string pointers for `base`, CPU, memory, and
ephemeral storage. `base` is a provider-defined selector, preserved exactly.
`Overlay(parent, patch)` copies fields; supplied scalar values replace inherited
values, including lower resource minima. Validate each input and the effective
result. Omission inherits; empty values are invalid. Packages and capabilities
retain their existing composition rules.

`Allocation` version 2 reports actual usable capacity and a concrete `base` name
from the provisioner. It must not be inferred from requests, the host, a previous
attempt, image names, or OS families. There are no aliases. Providers choose
supported names, backing environments, and their default when base is omitted.

`MatchBase` matches the entire name case-sensitively. Each `*` matches zero or more
characters, including `/`; all other characters are literal. `macos*` matches
`macos-123-456`, while `linux` does not match `ubuntu`. Exactly `*` accepts a
missing allocation base, as does an omitted requirement. Every other selector
requires a known base. Actual names cannot contain `*`. Selectors and concrete
names must be nonempty valid UTF-8, at most 4096 bytes, without NUL, space, tab,
CR, or LF. Shared cases live in [testdata/base_matches.json](testdata/base_matches.json).

`Compare` validates both operands and returns structured `base` mismatches with
`unknown_allocation` or `incompatible`. Resources and capabilities are checked
independently even when `base: "*"`. Compatibility is not a lease or reservation.

CPU accepts cores or millicores at integral millicore precision. Memory/storage
accept SI (`k`, `M`, `G`, `T`, `P`, `E`) or IEC (`Ki` through `Ei`) units and must
resolve to positive integral bytes within signed 64-bit range. Quantities are
never silently rounded down or capped; canonical byte values retain a unit.

Legacy demand/submission v3 and allocation v1 remain readable. Image/platform
normalization and comparison are isolated in [legacy.go](legacy.go), preserving
old directives and replay. Those deprecated fields are accepted for old work;
new requirements use `base`. Combining base with image/platform, including via
an overlay, is an error. Allocation image/platform facts can coexist with base
for draining old work, but never prove a base match. New base semantics require
v4 demand and v2 allocation; unknown versions are rejected. Upgrade consumers
before submitting base requirements.

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

New records use demand/submission **4** and allocation **2**. Legacy **3/1**
records retain their existing capability and image/platform checks. Capability
validation is unchanged by the base-selector migration.
