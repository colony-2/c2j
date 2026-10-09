# Proposal: versioned tools with just-in-time setup

Status: local worker implementation is described in [Execution tools](EXECUTION_TOOLS.md).
Pulse provider orchestration remains a proposal.

Use our own small package adapter layer for pnpm, Nix, and uv. See the
[provider caching note](PULSE_ENVIRONMENT_PREPARATION_AND_CACHING.md) for reuse and
[prior-art comparison](EXECUTION_ENVIRONMENT_TOOL_COMPARISON.md) for background.

## Declarations

Use `image` plus `packages`, without a separate `environment.base`:

```yaml
execution:                       # also supported under node execution_needs
  image: registry.example/nix-base:1
  packages:
    - nix:nixpkgs#jq
    - uv:ruff==0.11.2
    - pnpm:typescript@5.8.3
```

Extension ops declare dependencies with the same format:

```yaml
name: example
command: ["./run.sh"]
dependencies:
  - uv:ruff==0.11.2
```

Split references at the first colon and preserve native package syntax,
including `pnpm:@scope/tool@1.2.3`. Nix selects a flake revision and attribute,
not a universal `package@version`; for example,
`nix:github:NixOS/nixpkgs/<commit>#jq`. Reject malformed references and unknown
prefixes. Initial declarations are literal package references, not shell code.

Image/platform/resources retain existing override rules. Packages are additive
requirements across recipe, enclosing node, job, and op declarations; an image
change does not erase tool requirements. Deduplicate identical references and
retain declaration scope. Different versions can coexist in separate package
locations; do not reject a recipe merely because two ops use different versions.

## Native version-specific invocation

| Declaration | Example invocation |
| --- | --- |
| `uv:ruff==0.11.2` | `uvx --from 'ruff==0.11.2' ruff check .` |
| `pnpm:typescript@5.8.3` | `pnpm --package=typescript@5.8.3 dlx tsc --version` |
| `nix:github:NixOS/nixpkgs/<commit>#jq` | `nix run 'github:NixOS/nixpkgs/<commit>#jq' -- --version` |

These are existing runner patterns, not new c2j commands.
[uv](https://docs.astral.sh/uv/guides/tools/#requesting-specific-versions),
[pnpm](https://pnpm.io/cli/pnx),
[Nix](https://nix.dev/manual/nix/2.28/command-ref/new-cli/nix3-run)

Ops may invoke qualified runners directly. For scripts that call plain `ruff`,
`tsc`, etc., supply an invocation-local PATH selecting their declared versions.
The nearest declaration selects the default executable: op, node, job, recipe.
Other versions remain available through qualified invocation. If the same scope
requests two versions exporting the same executable, require qualified invocation
rather than silently choosing one.

The base image supplies Nix, pnpm/Node, uv/Python, and git. c2j prepares declared
packages using these existing managers; base-image construction is separate work.
The provider also supplies c2j and its supervisor. Preserve runtime paths and
make tools accessible to the actual task user.

## Preparation and timeout boundary

```text
reach task → check task-result cache
  hit  → replay result; no dependency resolution or setup
  miss → resolve/prepare active scope tools and op dependencies
       → schedule timed op task → execute → report diagnostics
```

- All dependency resolution and installation are lazy. Recipe, sequence, and
  other node declarations become available before the **first uncached task in
  that context**. Prepare applicable ancestor scopes too; entering a scope alone
  does not trigger setup. Fully cached or unvisited scopes require none.
- Resolve and prepare an op's dependencies only immediately before an uncached
  invocation. A task-result cache hit skips package lookups, installer bootstrap,
  installation, and readiness checks, including those for enclosing scopes.
- Share scope setup across dependent tasks and coalesce concurrent requests.
  Parallel live tasks wait for applicable scope setup. Reuse readiness on the
  current executor; a new executor or lost environment requires preparation only
  when the next live task needs it. Included recipes and child jobs follow the
  same rule in their own contexts.
- “Pre-run” means realizing the native runner's package environment ahead of
  execution, including downloads, builds, installation scripts, and activation.
  Prefer installation/realization APIs; a tool probe must be explicitly known
  to be side-effect-free. Do not assume every CLI supports `--version` or run
  the op's real command as a warmup.
- Preparation runs as a separate provisioning/setup step **before scheduling the
  timed op task**. It has its own timeout, honors cancellation and job deadlines,
  and does not consume the op task's execution timeout. Delaying only the child
  process timer is insufficient because c2j observes JobDB's durable deadline.
- Bind execution to the prepared version/environment. Warming a disposable
  runner cache alone is insufficient: retain it and prevent re-resolution or
  installation during timed execution. Where a runner cannot guarantee this,
  invoke the prepared executable/environment directly with equivalent version
  selection. Readiness loss returns work to preparation, not an in-task install.

A setup failure prevents the op from starting and is reported as a setup failure,
not an op timeout. Record it even when no op task is created. A genuinely retried
op repeats the readiness check; replay of completed work does not rerun setup.

## Setup diagnostics

At task completion, include setup metadata alongside execution timing, outside
user-defined op outputs. Preserve it for success, failure, timeout, and cancellation:

- `setup.wall_ms`: elapsed time for this invocation's preparation, including
  dependency resolution, bootstrap, installation, activation, and any wait for
  shared preparation.
- `setup.tools`: requested reference, prepared version/identity, elapsed time,
  and outcome (`prepared`, `reused`, or `failed`) for each tool.
- `setup.scope_setup_refs`: links to shared recipe/node scope setup diagnostics,
  including durations. Record shared setup once rather than charging its full
  duration to every task.

Use wall-clock elapsed time rather than summing parallel tool durations. Persist
records by setup attempt so retries and handoffs retain accurate diagnostics;
report task execution duration separately. Proposed field names are illustrative.
Cached replay preserves historical diagnostics and records no new setup attempt.

## Resolution and Pulse

c2j traverses the recipe as needed, using durable identities/results for replay;
there is no upfront resolution of the entire recipe/op tree. Load includes and
resolve selectors only when traversal or live execution needs them. Cached op
replay must not fetch a manifest merely to discover unused dependencies. Adapt
existing [include](pkg/worker/compiler/inline_resolution.go) and
[selector](pkg/worker/compiler/within_recipe_resolution.go) resolution accordingly.
Reading enough recipe structure to locate a cached result is distinct from
resolving its tools. Persist source/package selections when first resolved so
retries and handoffs reuse them. Unknown packages in unvisited scopes cause no
network lookup or setup failure; literal syntax can still be validated locally.

Pulse can run traversal/replay in a minimal c2j runner, independent of the selected
workload image. At an uncached task boundary, c2j publishes only the newly needed
scoped requirements and yields if preparation is required. Resolution/preparation
may run outside the workload container, but services that specific demand rather
than scanning the whole graph. Under normal job ownership, persist selections
and readiness before scheduling the timed op. Providers prepare or attach tools;
c2j enforces the barriers. A task-result hit never asks the provider for tools.

Providers own storage, caching, refresh, and eviction. Prepared tools can be
mounted, restored, or packaged; no image-building strategy is required. Mutable
references follow explicit provider refresh policy, not a fresh version lookup
inside each task. No new cross-provider cache or package-locking service is proposed.

Validate fully cached scopes (zero resolution), mixed cached/live tasks, lazy
included-op discovery, parallel first use, side-by-side versions, cache loss,
retries, setup failure, timeout exclusion, and diagnostic attribution.
