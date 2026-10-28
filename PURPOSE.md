# c2j

c2j provides the local job-oriented CLI and Go integration libraries for
submitting, executing, resuming, and inspecting recipe jobs through JobDB. Its
Go module is `github.com/colony-2/c2j`.

## Responsibilities and interfaces

- The [CLI](cmd/c2j/internal/cmd/root.go) exposes job submission, execution,
  listing, restart, goal management, and recipe testing, together with cell,
  configuration, secret, Git, and bundled skill tooling. Cell resolution uses
  `.c2/config.yaml` or supported project detection; see the [user guide](README.md).
- [Recipe definitions](pkg/recipe), the [compiler](pkg/worker/compiler), and
  [worker operations](pkg/worker/ops) implement recipe evaluation and execution,
  including replay and recovery, built-in operations, and extension invocation.
  Supporting packages handle artifacts, typed object checkpoints, input/review,
  permissions, secrets, Git workspaces, and tool environments.
- [Recipe job APIs](pkg/recipejob) let Go applications submit and integrate recipe
  jobs. The supported [job listing API](pkg/joblist/README.md) provides read-only
  remote job discovery and typed execution/completion views without starting a
  worker or embedded database.
- [Execution requirements](pkg/execution) describe resources, platform, and image
  needs. The [execution-aware runtime](pkg/executionruntime/README.md) checks
  unfinished tasks against executor allocation facts, publishes demand, and
  yields incompatible work for handoff. Replayed completed tasks do not request
  their historical environments.
- The CLI supports remote JobDB and a persistent embedded local runtime for
  development. [Dependencies](go.mod) include JobDB and strata-go; the
  [runtime integration](cmd/c2j/internal/swfruntime) wires execution to the backend.
- The [core interface layer](pkg/core/interfaces.go) retains graph construction
  and storage contracts for cell graphs, positions, and container mappings,
  alongside the [domain types](pkg/core/types.go).

## Ownership boundaries

c2j owns the recipe execution integration and CLI behavior in this repository.
JobDB supplies the persistent job/task backend; its server and storage contract
are maintained outside this repository. Applications such as Cortex consume
c2j's Go interfaces rather than transferring their application responsibilities
here.

External dispatchers such as Pulse can claim work and launch
[`c2j run with-lease`](GUIDE-Run-With-Lease.md). c2j checks execution compatibility
and reports handoffs; resource provisioning belongs to the surrounding executor
infrastructure. Commands and extensions run directly in the worker environment;
container isolation is supplied externally, as described in the
[operation sandboxing release notes](RELEASE-NOTES-Remove-Op-Sandboxing.md).

The default build recipe comes from `colony-2/recipes`; c2j owns recipe selection,
loading, and execution, while the shared workflow content belongs to that
repository. Custom recipe selection remains supported. Prebuilt extension
operations follow the [extension interface](EXTENSION_OPS.md), and external
execution tools follow the [tool environment contract](EXECUTION_TOOLS.md).

This document describes current responsibilities. Detailed command contracts
and supported API semantics remain in the linked guides and package documents.
