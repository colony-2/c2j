# c2j feature request: portable execution requirements and environment changes

Status: implemented in the [execution-requirements design](C2J_PORTABLE_EXECUTION_REQUIREMENTS_DESIGN.md) and [user guide](GUIDE-Execution-Tracking.md). The final contract requires an explicit yield for any runtime requirement change, including when the current allocation is sufficient. JobDB's existing client-payload reschedule API is sufficient.

## Problem

A recipe may know its execution requirements in advance or discover them while running. For example, a job can inspect an input manifest using 2 GiB of memory and then determine that processing it requires 16 GiB, more scratch space, or a different container image.

c2j should let that job continue in a suitable environment while retaining its identity, recipe, and durable progress. The author should not have to generate a replacement recipe, submit a replacement job, or manually reconstruct prior work to change resources.

## Model

Distinguish three things:

| Concept | Meaning |
| --- | --- |
| Initial requirements | Optional requirements declared by the recipe for starting its work. |
| Current requirements | The job's effective requirements for continuing execution, including accepted changes requested while running. |
| Allocated environment | The actual image, platform, and resource allocation supplied to an execution attempt. |

The recipe is a useful source of initial requirements. The job is the authority for current requirements. A running job can change the latter without editing the former. Current requirements must be durably associated with the job and discoverable alongside its metadata.

A recipe without declarations remains valid. Unspecified fields use the execution environment's configured defaults. Defaults must not erase explicit requirements.

## Portable vocabulary

Use a small vocabulary based on established container conventions: Kubernetes-style CPU and byte quantities, plus OCI image and platform identities. This combines familiar conventions; it is not a claim that one existing specification covers all execution backends.

Proposed author-facing terms and meanings:

| Requirement | Example | Meaning |
| --- | --- | --- |
| `cpu` | `"2"`, `"500m"` | Minimum allocated CPU capacity for the executor. One unit is one vCPU; `1000m` is one unit. This is capacity, not a portable performance benchmark. |
| `memory` | `"4Gi"` | Minimum allocated RAM for the executor, including c2j and the recipe's processes. |
| `ephemeral-storage` | `"10Gi"` | Minimum usable temporary workspace for execution. It has no persistence guarantee between execution attempts. |
| `platform` | `"linux/amd64"`, `"linux/arm64"` | Required operating system and CPU architecture, with an architecture variant where necessary. |
| `image` | `"registry.example/recipe-runner@sha256:…"` | Container image providing the execution environment. It must support c2j execution and the recipe's tools. |

CPU quantities use cores and millicores. Memory and storage use byte quantities with explicit units, such as `Mi` and `Gi`; `1Gi` is 1,073,741,824 bytes. Values must be positive when supplied. Omission means unspecified, not zero. Equivalent representations must retain the same meaning. Kubernetes uses these resource names and quantity conventions. See [Kubernetes resource management](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/).

Platform names should use OCI operating-system and architecture values, including `amd64` and `arm64`. The chosen image must contain a compatible platform. Prefer immutable image digests; if mutable tags are accepted, the resolved image identity must be available for diagnosing and resuming an execution. See [OCI image platform definitions](https://github.com/opencontainers/image-spec/blob/main/image-index.md).

An illustrative declaration, with final syntax left to c2j:

```yaml
execution:
  image: registry.example/recipe-runner:1.2
  platform: linux/amd64
  resources:
    cpu: "2"
    memory: "4Gi"
    ephemeral-storage: "10Gi"
```

### Matching semantics

- CPU, memory, and temporary storage are minimum capacities. An environment may allocate more to fit a supported machine size. They are not separate burst limits or promises of exclusive physical hardware.
- Image and platform are compatibility constraints, not numeric minima. More CPU cannot compensate for an incompatible image or architecture.
- Temporary workspace must remain usable alongside the requested application memory. If storage is memory-backed, an environment must account for both demands instead of treating the same bytes as satisfying both requirements.
- Provider overhead, including image layers and ancillary processes, must not silently reduce the required usable workspace. The execution environment reports the allocation it actually supplies.
- A backend that cannot satisfy a declaration reports that incompatibility. It must not silently omit a requirement or start dependent work with fewer resources.
- Provider, region, account, identity, networking, and credentials remain deployment configuration. They are not part of the initial portable resource vocabulary. Execution deadlines and retry policies retain their existing meanings separately.

### Mapping to common systems

These mappings explain the vocabulary's portability; c2j is not being asked to implement cloud provisioning.

| System | Mapping and qualification |
| --- | --- |
| Kubernetes | CPU, memory, and `ephemeral-storage` requests express scheduling demand; image and node/platform selection express compatibility. Resource limits and usable-workspace overhead remain deployment policy. |
| ECS/Fargate | Task CPU and memory select a supported size; 1 vCPU corresponds to 1024 ECS CPU units. Architecture maps to the task runtime platform. Ephemeral storage must account for image storage as well as workspace. |
| Cloud Run Jobs | CPU and memory map to job container allocation. Filesystem writes consume memory, so temporary-workspace requirements may need extra memory or another suitable storage arrangement. |
| Azure Container Apps Jobs | CPU and memory map to container resources. Ephemeral storage capacity depends on the configured execution environment; it cannot be assumed to be an independently adjustable disk size. |

Fargate constrains CPU/memory combinations and counts container image storage against ephemeral capacity. See [task sizing](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task_definition_parameters.html) and [ephemeral storage](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/fargate-task-storage.html). Cloud Run documents filesystem writes as part of memory usage in [job memory configuration](https://docs.cloud.google.com/run/docs/configuring/jobs/memory-limits). Azure describes temporary storage and its relationship to CPU allocation in [Container Apps storage](https://learn.microsoft.com/en-us/azure/container-apps/storage-mounts).

## Requirement 1: Declare and expose execution requirements

### Required behavior

A recipe can optionally declare initial execution requirements. c2j associates the applicable requirements with each submitted job and exposes its current requirements in machine-readable job listings, alongside existing identity and readiness information.

### Acceptance criteria

- Every recipe form can carry the same optional requirements with consistent meaning.
- Declared requirements become available before recipe work that depends on them executes. If recipe resolution is deferred until execution, resolution may use a default bootstrap environment, but dependent work must wait for a compatible environment.
- Once a recipe version is resolved for a job, later changes to the source recipe do not silently change that job's recorded requirements.
- Existing repository filtering and pagination continue to work. Requirements are available for listed jobs without interpreting recipe history or loading the submitter's local files.
- Missing requirements are distinguishable from failed retrieval, malformed data, and unsupported schema versions.
- Partial requirements remain partial until environment defaults are applied; explicit requirements take precedence over those defaults.
- A separately submitted child job starts with its own recipe's initial requirements. A parent's runtime resource change does not implicitly change the child's environment requirements.
- Reusing a recipe inline within one job does not imply a second executor; any unsatisfied environment requirement follows the same request-and-yield behavior described below.
- An explicit restart preserves the job's effective requirements unless the caller deliberately chooses a documented alternative. Ordinary resumption always retains accepted changes.
- Existing recipes without requirements remain usable with configured defaults.

## Requirement 2: Request a different environment and resume the same job

### Required behavior

Executing recipe work can request changed execution requirements based on information discovered at runtime. The executor yields with the new requirements before dependent work. Changing requirements is a normal continuation of the same job.

Example:

1. A recipe starts with 2 GiB of memory and durably records an input manifest.
2. It determines that the next operation needs 16 GiB and requests that capacity.
3. The current environment is insufficient, so the job yields before starting the dependent operation. Its current requirements now show 16 GiB.
4. A compatible execution attempt resumes the same job, uses the recorded manifest, and continues the operation.
5. If the original environment already had sufficient resources, it still yields to publish the change. The next invocation can use that same environment.

### Acceptance criteria

- Requesting an environment change preserves job identity, repository identity, recipe identity, prior durable results, and artifact references.
- A change to one requirement preserves the other current requirements. Explicit clearing or replacement, if supported, has documented semantics.
- Accepted changes remain visible and effective after a crash or executor loss, including when the requesting environment was already sufficient.
- A job must never become available for dependent work with its accepted new requirements lost or replaced by stale requirements. Persisted progress and requirements describe a consistent continuation point.
- The requesting execution stops before dependent work when its environment is insufficient. Once it yields ownership, it cannot continue executing that work.
- An old or delayed executor cannot perform work that needs resources it does not provide. Compatibility with current requirements is checked as part of establishing execution ownership and before continuing resource-dependent work; an earlier read alone is insufficient.
- Executors can make their allocated environment known to c2j. The requested environment and the actual allocation are distinguishable, including when a provisioner rounds allocations up.
- Reprocessing an accepted resource request during recovery or normal workflow replay does not create another job, overwrite a newer requirement, or cause an endless yield loop. Completed durable operations retain c2j's normal replay behavior.
- A crash during a transition leaves a recoverable state. Retrying the request does not lose progress or apply conflicting environment changes.
- Cancellation remains authoritative during the transition. Losing execution ownership prevents an old executor from modifying current requirements.
- Unsupported or disallowed requests are observable as unmet requirements or explicit errors. Work must not silently continue in an insufficient environment.
- A local runner follows the same compatibility rules. If it cannot provide the requested environment, it reports that condition instead of pretending a migration occurred.

### Checkpoint boundary

This is continuation from durable workflow state, not live migration of a process. A request must take effect at a point where c2j can resume the work safely. In-memory values, open connections, and temporary files are not automatically transferred; data needed after yielding must be persisted through the normal durable mechanisms.

An operation that requests resources midway through non-checkpointed work may need to retry that operation. This feature does not promise exactly-once external side effects. It also does not automatically infer new requirements after an out-of-memory kill: a dead process cannot issue a request. Proactive requests and recovery policy are separate concerns.

Image or platform changes must preserve compatibility with the job's pinned recipe and durable history. Updating recipe logic is a separate operation from changing its execution environment.

## Requirement 3: Make environment-related outcomes clear to unattended callers

Targeted execution should distinguish completion, ordinary yield, waiting for a suitable environment, contention/no work, and actual execution failure. A normal environment handoff should be representable as successful container termination while still reporting its specific outcome to a machine-readable caller.

An executor that cannot satisfy current requirements returns promptly without waiting indefinitely, claiming unrelated work, or prompting for input. A machine-readable listing must make current requirements available whether work needs its first executor or a replacement environment.

## Scope and implementation baseline

The initial feature concerns one job's current environment requirements. Provider selection, cloud resource management, placement algorithms, and live container resizing are outside this request. An environment change can be fulfilled by a replacement executor; in-place resizing is not required.

At reviewed c2j commit `1e2a8088bb86ba7d3b81e7de94ee2fda9dff6e9d`, recipe metadata has no such portable declaration, listing omits execution requirements, and targeted execution already delegates leasing and continuation to jobdb. Relevant sources: [recipe model](https://github.com/colony-2/c2j/blob/1e2a8088bb86ba7d3b81e7de94ee2fda9dff6e9d/pkg/recipe/recipe.go), [listing](https://github.com/colony-2/c2j/blob/1e2a8088bb86ba7d3b81e7de94ee2fda9dff6e9d/cmd/c2j/internal/listjobs/service.go), and [targeted execution](https://github.com/colony-2/c2j/blob/1e2a8088bb86ba7d3b81e7de94ee2fda9dff6e9d/cmd/c2j/internal/runjob/service.go).

The declaration/listing functionality and runtime handoff can be delivered separately. Safe handoff requires the compatibility and recovery behaviors above; existing targeted leasing alone is not evidence that resource requirements are enforced.
