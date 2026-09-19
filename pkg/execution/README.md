# Portable execution model

This package implements the shared value model for portable execution
requirements. It does not provision environments, change JobDB lease matching,
or enable recipe execution handoff by itself. JobDB now provides independent
client payload and explicit workflow yield, and c2j's context wrappers support
them; the execution-requirements feature itself remains unintegrated. See the
[upgrade notes](../../JOBDB_UPGRADE_NOTES.md) for the current contract and known
task-route incompatibility, and the
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
