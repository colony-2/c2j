# Feature request: portable execution capabilities in c2j

Status: implemented in c2j, 2026-11-01. Pulse/provider integration remains downstream work.

## Problem and requested outcome

Jobs need to express execution requirements such as “can run Docker containers” or “can create KVM virtual machines.” Image, platform, CPU, memory, storage, and installed packages cannot express these requirements. Installing a Docker client does not establish access to a usable container engine; installing a VM tool does not establish usable virtualization support.

Add a generic `capabilities` set to c2j execution requirements and executor allocation facts. c2j should preserve and compare capability names without implementing Docker, KVM, or provider-specific provisioning logic. Providers supply the promised behavior and report the capabilities actually available to the execution.

A job should request the behavior it needs. “Privileged container,” “host Docker socket,” and “nested virtualization enabled” describe possible provider implementations. They are poor substitutes for the job's required interface.

## Reviewed baseline

This request is based on the c2j version pinned by Pulse in `go.mod`: `v0.0.108-0.20261029180024-7af6c5c2f09a` (commit `7af6c5c2f09a`). It does not claim to describe later c2j versions.

- `pkg/execution/requirements.go` defines typed `Requirements` containing packages, image, platform, and resources, and typed `Allocation` facts. Neither has a capability set.
- `pkg/recipe/execution_needs.go` explicitly rejects unknown `execution_needs` fields. This declaration is not a freeform metadata object.
- `pkg/execution/compatibility.go` provides requirement/allocation comparison.
- `pkg/worker/compiler/execution_requirements.go` reconstructs effective requirements from recipe, current node, and job requirements. Runtime directives are reconstructed from durable results; allocation checks belong to guarded workers for unfinished work.
- Execution data uses strict decoding and exact schema-version checks. Keep schema version numbers unchanged and add capabilities as optional fields; omitted capability fields decode as empty sets.

## Proposed declarations

Support capabilities wherever c2j already accepts execution requirements, including recipe `execution`, node `execution_needs`, submission requirements, and the public runtime requirement-change API.

Recipe-wide example:

```yaml
id: container-work
version: "1.0.0"
input_schema: {}
execution:
  capabilities:
    - docker
    - docker.workspace-mounts
sequence: []
outputs: {}
```

An illustrative node requirement fragment:

```yaml
execution_needs:
  capabilities:
    - virtualization.kvm
```

The public Go requirement and allocation types should each expose `Capabilities []string`, with consistent JSON/YAML encoding where applicable. This is a generic field: introducing another capability name must not require a c2j schema or code change.

## Capability names and contracts

Treat names as opaque, case-sensitive identifiers with exact matching. Validate a documented identifier syntax; a suitable initial grammar is `[a-z][a-z0-9]*(?:[._/-][a-z0-9]+)*`. Reject malformed names and non-string values. Accept unfamiliar well-formed names. Deduplicate and sort for stable serialization and comparison.

Dots and slashes convey no implicit hierarchy. Reporting `docker.workspace-mounts` does not implicitly report `docker`; providers report both when both contracts are satisfied. Do not add wildcard matching, version ranges, implication rules, or arbitrary predicates in this feature.

Document a small initial vocabulary separately from the matching implementation:

| Proposed name | Contract supplied to the task |
| --- | --- |
| `docker` | A configured Docker Engine API connection usable by the task identity to pull images and create, start, inspect, stop, and remove containers. Provider lifecycle handling cleans up job-owned resources. |
| `docker.workspace-mounts` | Docker containers can bind-mount the task workspace using the paths visible to the task, with documented ownership and access behavior. |
| `docker.privileged-containers` | The supplied Docker service permits creating privileged child containers. This describes those child containers, not the privilege mode of the c2j executor. |
| `virtualization.kvm` | The task identity can use the KVM API to create and run guests, including usable device access and permissions. Device-file existence alone is insufficient. |

These names describe baseline contracts, not every possible Docker or virtualization feature. Providers must document supported platforms and limitations; additional workload requirements need additional explicit contracts. A Docker CLI, Compose, build tools, QEMU, or other client software remains an image/package responsibility. The `docker` capability alone does not promise workspace bind mounts, reachable published ports, privileged containers, or image-building extensions.

A VM, configured container, or attached service may satisfy a contract. c2j must not infer capability availability from environment type, UID, package presence, CPU flags, or a provider name. Provider policy separately determines which implementations are allowed.

## Composition and matching

Use additive set semantics for capability requirements:

- Effective capabilities are the union of recipe requirements, applicable enclosing/current node requirements, and job requirements.
- Submission and runtime requirement patches add capabilities; omission or an empty list adds nothing. They cannot erase another declaration's requirement. Capability removal is outside this initial request.
- Node contributions follow existing execution scope behavior. Leaving a node restores the enclosing contribution; a node-only requirement must not leak into an unrelated sibling. This is distinct from an explicit durable job requirement change.
- Includes, child groups, and separately submitted child jobs retain their existing execution inheritance rules. Carry the new field through those paths without introducing new cross-job inheritance.
- For requirements `R` and supplied capabilities `A`, compatibility requires `R` to be a subset of `A`. Extra supplied capabilities are acceptable. An omitted allocation field establishes no capabilities and cannot satisfy a nonempty requirement.

Keep existing image, platform, resource, and package semantics unchanged. Capability-only requirements must count as execution constraints, including checks that require an execution-aware runtime and guarded task workers.

Report each missing capability in structured compatibility diagnostics. A missing capability must prevent dependent live work and use the existing `environment_required` handoff path. Preserve job identity, results, and the full effective demand. Do not encode capabilities into JobDB task routes or `NextNeed`.

## Executor reporting and public interfaces

Allow trusted executor configuration to supply capability facts through the public Go API and all existing allocation-aware run paths, including supplied-lease execution and embedded workers. Provide CLI/environment equivalents; proposed names are repeatable `--execution-capability NAME` and `C2J_EXECUTION_CAPABILITIES` containing a JSON string array.

Define deterministic precedence: if CLI capability flags are supplied, their combined set replaces the environment-provided capability set. Neither source merges with the requested set. A requirement is never evidence that the executor supplies it.

Provide equivalent submission support, such as repeatable `--require-capability NAME`. Runtime directives should use the existing public requirements API. CLI names may follow established c2j conventions, but all supported entry points must agree on semantics.

Reported capabilities are facts for the current execution attempt. Do not reuse a prior executor's facts after handoff. Provisioners are responsible for preparation, readiness checks, and accurate reporting; c2j does not need built-in Docker/KVM probes. Capability declarations do not authorize privileged provisioning or bypass provider policy.

## Persistence, replay, and visibility

Carry capabilities through normalization, cloning, overlay, effective-demand calculation, submission metadata, durable requirement directives, handoff serialization, listing projections, and executor allocation decoding. Include them in equality/change detection so capability changes cannot disappear as apparent no-ops.

Preserve existing live-work boundaries. Cached work may replay without provisioning the capability used by the original execution. Replaying a recorded requirement directive must still reconstruct its effect for subsequent live work. Fully cached execution must not trigger new capability provisioning solely because historical work required it.

Expose required and supplied capabilities in the existing public execution types, JSON output, and mismatch/handoff diagnostics. A provisioner reading the supported listing API must see the same effective requirement that the worker enforces, without parsing recipe YAML itself.

## Hard cutover and existing jobs

Assume a coordinated hard cutover: Pulse, Cortex, c2j, and all other producers and consumers are updated together. Mixed-version operation, old binaries reading new records, rolling-upgrade negotiation, and downgrade support are outside this request. Use the capability-aware encoding throughout the updated programs, retaining the existing schema version numbers.

Updated programs must still read, list, replay, and resume existing jobs in their pre-change encoding without rewriting stored jobs. An omitted `capabilities` field means an empty set of requirements or allocation facts. Existing recipes, submission metadata, durable requirement directives, demand snapshots, and allocation records without capabilities retain their behavior. Historical allocation facts remain historical; resumption uses the current executor's facts as usual.

Do not bump schema version numbers for this feature: retain demand/submission version 3 and allocation version 1 from the reviewed baseline. Add capabilities as optional fields within those versions. Omitted fields naturally decode as empty slices and must have empty-set semantics throughout normalization and matching. Existing version checks can remain unchanged. No version migration, general migration framework, or consumer-release compatibility matrix is required. Preserve existing validation and distinctions between unsupported, invalid, unresolved, and genuinely empty requirements.

Test pre-change records with the updated programs, covering listing, persisted demand, submission, durable directives, allocation, and supplied-lease resumption. Verify that omitted capabilities behave as empty sets and that existing jobs can continue running. Tests involving old binaries consuming new records are unnecessary.

## Downstream responsibilities and scope

Pulse will need corresponding changes to translate c2j capabilities into its provider-neutral launch request, preserve them during lease-based demand refresh and fallback, and report only capabilities established for that launch. Providers unable to fulfill a requested contract should return the existing `unsupported` outcome. These are follow-up Pulse/provider changes, not c2j provisioning responsibilities.

This request does not add a provider registry, Docker/KVM setup, arbitrary host flags, automatic privilege escalation, capability discovery, resource budgeting for child containers/VMs, or a general constraint language. Providers remain responsible for accounting, isolation, and cleanup of resources used to implement their contracts. No new JobDB scheduling field or capability-specific JobDB API is requested.

## Acceptance criteria

1. Recipe, node, submission, and runtime APIs accept capability sets; malformed values fail validation and unfamiliar valid names survive round trips unchanged.
2. Normalization deduplicates and orders names. Matching accepts supersets and reports missing capabilities; prefixes and dotted names have no implied relationship.
3. Recipe/node/job composition is additive. Tests cover nested scopes, sibling restoration, includes, child-group propagation under existing rules, and child-job independence.
4. Capability-only live work requires a guarded execution runtime. Missing or unknown allocation capabilities yield before task side effects; a compatible executor resumes the same job successfully.
5. Cached work replays without new provisioning, while recorded directives still affect later live work. Handoff, retry, and restart reconstruct requirements without reusing stale allocation facts.
6. Public listing, demand snapshots, handoff diagnostics, CLI output, embedded APIs, and supplied-lease execution preserve and agree on capabilities. CLI/environment precedence is tested.
7. Schema version numbers remain unchanged. Updated programs read, list, replay, and resume existing jobs in the pre-change encoding without stored-data migration; omitted capabilities mean empty sets. All programs are upgraded together; mixed-version and downgrade support are outside scope.
8. Provider-neutral tests use synthetic names and supplied allocation facts, requiring no Docker daemon or KVM host. Documentation shows how a provider can fulfill the Docker and KVM contracts through different implementations.
