# C2J Op-Visible Paths Implementation Spec

Status: draft

Source requirement: `REQUIREMENTS_C2J_OP_VISIBLE_PATHS.md`

## Assessment

The feature request is appropriate. Recipe authors and extension authors need a
stable way to refer to the workdir, worktree, artifact inbox, and artifact
outbox from the process that is actually running the op.

The implementation should avoid two bad outcomes:

- Template rendering should not directly branch on another authored variable
  like `inputs.sandbox.type`.
- c2j should not invent a new reserved sandbox filesystem layout. Existing
  Codex/Shai path conventions such as `/src/inbox` and `/src/outbox` should stay
  intact when running sandboxed.

Recommended approach: add `context.environment.op.*` as a stable author-facing
namespace that renders to c2j-owned sentinels. At execution time, the registered
op may implement a path transformer. The transformer reads structured sandbox
path config, produces sentinel replacements, and returns any mount metadata the
sandbox runner needs.

This means we update sentinel replacement values globally across the op input
payload. We do not mutate a specific location in the template context tree.

## Options Considered

Option 1: make the existing flat variables change based on `inputs.sandbox`.
This is not recommended. It would make `context.environment.inbox` sometimes
mean a host path and sometimes mean a sandbox path, which is surprising and
breaks existing recipes that depend on host-view behavior.

Option 2: add separate static sandbox variables such as
`context.environment.sandbox.*`. This is simpler but still leaves recipes and
extension defaults choosing between host and sandbox paths explicitly. It also
does not solve defaults cleanly because the final path view is only known by the
op that creates the process.

Option 3: add `context.environment.op.*` backed by late op-visible sentinels and
let process-running ops provide a path transformer. This is the selected
approach. Authors ask for the path as seen by the process, while the op that
creates that process decides whether the value is a host path or sandbox path.

Option 4: rewrite only known extension input fields after validation. This is
too narrow because sentinels can appear in prompts, env vars, arrays, nested
maps, schema defaults, and arbitrary strings.

## Decision

Implement `context.environment.op.*` through late-bound sentinel replacement and
a structured sandbox path config.

1. Keep existing flat fields as host-view paths:
   - `context.environment.workdir`
   - `context.environment.worktree_path`
   - `context.environment.inbox`
   - `context.environment.outbox`
2. Add `context.environment.host.*` as explicit aliases for the same host-view
   paths.
3. Add `context.environment.op.*` as process-view paths, backed by new
   op-visible sentinels during template rendering.
4. Let process-running ops implement an optional path transformer.
5. The transformer uses structured sandbox path config to map host paths to
   sandbox-visible paths.
6. Use shared command execution/path-mapping code for `command_execution` and
   selector-backed extension execution. Extension execution is a specialization
   of command execution, not a separate process model.

## Current Architecture

Current path handling is already late:

- The recipe worker seeds `context.environment.*` with sentinels such as
  `__C2_SENTINEL_WORKTREE__`.
- The compiler resolves templates and extension defaults before concrete
  operation directories exist.
- The activity executor later creates:
  - `workdir`
  - `worktree_path`
  - `inbox`
  - `outbox`
- The activity executor replaces existing host sentinels with concrete host
  paths immediately before invoking the op.
- Selector-backed extension ops parse reserved `inputs.sandbox` inside the
  extension execution op, after compiler-time defaulting and template rendering.

The new op-visible paths should follow that model: render symbolic sentinels
early, resolve them late.

The requirements phrase this as resolving the effective sandbox before path
rendering. In this codebase, "rendering" should be interpreted as final sentinel
hydration, not compiler-time template interpolation. Compiler-time interpolation
can safely produce op-visible sentinels; the executor resolves those sentinels
after it knows the effective sandbox and concrete host directories.

## Path Model

Host paths are engine-created paths on the worker host:

```text
/tmp/recipe-worktree-123
/tmp/recipe-worktree-123/worktree
/tmp/recipe-worktree-123/inbox
/tmp/recipe-worktree-123/outbox
```

Op-visible paths are the paths a process can use from inside its actual
execution environment.

For direct execution:

```text
host inbox = /tmp/recipe-worktree-123/inbox
op inbox   = /tmp/recipe-worktree-123/inbox
```

For Shai/Codex sandbox execution, preserve the existing path pattern:

```text
host inbox = /tmp/recipe-worktree-123/inbox
op inbox   = /src/inbox

host outbox = /tmp/recipe-worktree-123/outbox
op outbox   = /src/outbox
```

There is no new reserved c2j sandbox root such as `/c2j/op`.

## Structured Sandbox Path Config

The existing sandbox input is too loose for this behavior. Add structured path
mapping fields to the shared `SandboxInput` used by command execution and
selector-backed extension execution. It can move out of `pkg/ops/extensions` as
part of extracting the shared process runner.

Suggested shape:

```go
type SandboxInput struct {
    Type         string             `json:"type,omitempty" yaml:"type,omitempty"`
    Paths        *SandboxPathConfig `json:"paths,omitempty" yaml:"paths,omitempty"`
}

type SandboxPathConfig struct {
    Workdir      SandboxPathMapping `json:"workdir,omitempty" yaml:"workdir,omitempty"`
    WorktreePath SandboxPathMapping `json:"worktree_path,omitempty" yaml:"worktree_path,omitempty"`
    Inbox        SandboxPathMapping `json:"inbox,omitempty" yaml:"inbox,omitempty"`
    Outbox       SandboxPathMapping `json:"outbox,omitempty" yaml:"outbox,omitempty"`
}

type SandboxPathMapping struct {
    Host    string `json:"host,omitempty" yaml:"host,omitempty"`
    Sandbox string `json:"sandbox,omitempty" yaml:"sandbox,omitempty"`
    Mode    string `json:"mode,omitempty" yaml:"mode,omitempty"`
}
```

`Host` is the source path on the worker host. `Sandbox` is the target path
inside the sandbox. `Mode` is `ro` or `rw`; omitted means `rw` for these
operation paths.

Do not keep `InlineConfig` or any equivalent arbitrary Shai config escape hatch
in this path. Unsupported sandbox fields should fail validation. Future sandbox
capabilities should be added as named structured fields.

Defaults should be applied by c2j after concrete host operation directories
exist.

### Default Context Values

The path transformer must produce default `context.environment.op.*`
replacement values for every supported `inputs.sandbox.type`.

The existing flat variables and the explicit host namespace are host-view paths
for every supported sandbox value:

```text
context.environment.workdir             = <host workdir>
context.environment.worktree_path       = <host worktree_path>
context.environment.inbox               = <host inbox>
context.environment.outbox              = <host outbox>

context.environment.host.workdir        = <host workdir>
context.environment.host.worktree_path  = <host worktree_path>
context.environment.host.inbox          = <host inbox>
context.environment.host.outbox         = <host outbox>
```

For omitted `inputs.sandbox`, empty `inputs.sandbox.type`, or
`inputs.sandbox.type: none`:

```text
context.environment.op.workdir       = <host workdir>
context.environment.op.worktree_path = <host worktree_path>
context.environment.op.inbox         = <host inbox>
context.environment.op.outbox        = <host outbox>
```

No sandbox mounts are required.

The structured path defaults are:

```text
paths.workdir.host          = <host workdir>
paths.workdir.sandbox       = <host workdir>

paths.worktree_path.host    = <host worktree_path>
paths.worktree_path.sandbox = <host worktree_path>

paths.inbox.host            = <host inbox>
paths.inbox.sandbox         = <host inbox>

paths.outbox.host           = <host outbox>
paths.outbox.sandbox        = <host outbox>
```

For `inputs.sandbox.type: shai`:

```text
context.environment.op.workdir       = /src
context.environment.op.worktree_path = /src/git
context.environment.op.inbox         = /src/inbox
context.environment.op.outbox        = /src/outbox
```

The structured path defaults are:

```text
paths.workdir.host          = <host workdir>
paths.workdir.sandbox       = /src

paths.worktree_path.host    = <host worktree_path>
paths.worktree_path.sandbox = /src/git

paths.inbox.host            = <host inbox>
paths.inbox.sandbox         = /src/inbox

paths.outbox.host           = <host outbox>
paths.outbox.sandbox        = /src/outbox
```

The `/src/inbox` and `/src/outbox` defaults preserve the existing Codex
sandbox contract. The `/src/git` default for `worktree_path` matches the prior
Codex sandbox convention. There is no second workdir model for extensions;
these paths describe the command process view.

## Authoring Contract

Recipes and extension schema defaults should use:

```text
context.environment.op.workdir
context.environment.op.worktree_path
context.environment.op.inbox
context.environment.op.outbox
```

Example extension defaults:

```yaml
input_schema:
  properties:
    artifact_inbox_path:
      type: string
      default: "{{ context.environment.op.inbox }}"
    artifact_outbox_path:
      type: string
      default: "{{ context.environment.op.outbox }}"
```

Recipe prompt example:

```yaml
prompt: |
  Input artifacts:
  - `{{ context.environment.op.inbox }}/requirements/plan.json`

  Required output artifacts:
  - `{{ context.environment.op.outbox }}/implementation/latest-status.json`
```

Existing recipes using flat host paths stay backward compatible.

## Implementation Plan

### 1. Add Op-Visible Sentinels

Extend `pkg/contextual/context.go`:

```go
const OpWorkdirPathSentinel = "__C2_SENTINEL_OP_WORKDIR__"
const OpWorktreePathSentinel = "__C2_SENTINEL_OP_WORKTREE__"
const OpArtifactInboxSentinel = "__C2_SENTINEL_OP_ARTIFACT_INBOX__"
const OpArtifactOutboxSentinel = "__C2_SENTINEL_OP_ARTIFACT_OUTBOX__"
```

The existing sentinels remain host-view sentinels.

### 2. Extend Environment Context

Add nested path views:

```go
type EnvironmentPathContext struct {
    Workdir      string `json:"workdir,omitempty"`
    WorktreePath string `json:"worktree_path,omitempty"`
    Inbox        string `json:"inbox,omitempty"`
    Outbox       string `json:"outbox,omitempty"`
}

type EnvironmentContext struct {
    WorktreePath   string `json:"worktree_path,omitempty"`
    WorkdirPath    string `json:"workdir,omitempty"`
    ArtifactInbox  string `json:"inbox,omitempty"`
    ArtifactOutbox string `json:"outbox,omitempty"`
    Host           EnvironmentPathContext `json:"host,omitempty"`
    Op             EnvironmentPathContext `json:"op,omitempty"`
}
```

Seed values as:

```text
environment.workdir             = __C2_SENTINEL_WORKDIR__
environment.host.workdir        = __C2_SENTINEL_WORKDIR__
environment.op.workdir          = __C2_SENTINEL_OP_WORKDIR__

environment.worktree_path       = __C2_SENTINEL_WORKTREE__
environment.host.worktree_path  = __C2_SENTINEL_WORKTREE__
environment.op.worktree_path    = __C2_SENTINEL_OP_WORKTREE__

environment.inbox               = __C2_SENTINEL_ARTIFACT_INBOX__
environment.host.inbox          = __C2_SENTINEL_ARTIFACT_INBOX__
environment.op.inbox            = __C2_SENTINEL_OP_ARTIFACT_INBOX__

environment.outbox              = __C2_SENTINEL_ARTIFACT_OUTBOX__
environment.host.outbox         = __C2_SENTINEL_ARTIFACT_OUTBOX__
environment.op.outbox           = __C2_SENTINEL_OP_ARTIFACT_OUTBOX__
```

Update `pkg/template/template_interpolate.go` so Go-template rendering exposes
the nested `host` and `op` maps. CEL/native access should work through the typed
context once the struct is extended.

### 3. Add Operation Path Metadata

Add internal path types in `pkg/ops`:

```go
type OperationPaths struct {
    Workdir      string
    WorktreePath string
    Inbox        string
    Outbox       string
}

type OperationPathViews struct {
    Host OperationPaths
    Op   OperationPaths
}
```

Expose host operation paths through an optional dependency interface:

```go
type OperationPathProvider interface {
    OperationPaths() OperationPaths
}
```

Do not add this method directly to `OpDependencies` in v1. That would break
test and external implementations of the interface.

Update `pkg/worker/ops/op_executor.go` to build `OperationPaths` after creating
the operation directories and attach them to the concrete dependency object.

### 4. Add Optional Operation Path Transformer

Add an optional op interface in `pkg/ops`. The activity executor type-asserts
the registered op and calls it before invoking the task step:

```go
type RequiredMount struct {
    Source string
    Target string
    Mode   string
}

type OperationPathRuntime struct {
    Views  OperationPathViews
    Mounts []RequiredMount
}

type OperationPathTransformRequest struct {
    Input map[string]interface{}
    Host  OperationPaths
}

type OperationPathTransformResult struct {
    Runtime      OperationPathRuntime
    Replacements map[string]string
}

type OperationPathTransformer interface {
    TransformOperationPaths(context.Context, OperationPathTransformRequest) (OperationPathTransformResult, error)
}
```

The transformer returns replacement values for sentinels. The executor applies
those replacements recursively to the full op input map. This handles every
place the sentinel appears, including prompts, defaults, env vars, nested maps,
arrays, and strings containing a sentinel as a substring.

### 5. Hydration Order

Update `pkg/worker/ops/op_executor.go` to run hydration in two phases.

First, replace host-view sentinels as today:

```text
__C2_SENTINEL_WORKDIR__
__C2_SENTINEL_WORKTREE__
__C2_SENTINEL_ARTIFACT_INBOX__
__C2_SENTINEL_ARTIFACT_OUTBOX__
__C2_SENTINEL_JOB_ID__
```

Then, if the registered op implements `ops.OperationPathTransformer`, call it
with the host-hydrated input and host operation paths. Apply the returned
`Replacements` recursively to the full op input map.

If any `__C2_SENTINEL_OP_*` value remains after this phase, fail before
`Step.Invoke`:

```text
op-visible path resolution failed: operation "x" does not support context.environment.op.*
```

### 6. Store Runtime Path Metadata On Dependencies

The process-running op needs the mount metadata returned by the transformer.
Expose it through another optional dependency interface:

```go
type OperationPathRuntimeProvider interface {
    OperationPathRuntime() OperationPathRuntime
}
```

`op_executor` stores the transformer result on the concrete dependency object
before invoking the step. The shared command execution runner reads this
metadata and passes the mounts to process execution.

### 7. Extension Execution Transformer

`pkg/ops/extensions.GetExecutionOp()` should return a wrapper type that
delegates `RegisterableOp` methods and implements `ops.OperationPathTransformer`.
`command_execution` should use the same transformer implementation. The
extension wrapper only adds selector-aware input handling around that shared
logic.

Transformer behavior:

1. Parse reserved `inputs.sandbox`.
2. Apply structured sandbox path defaults from the host operation paths.
3. Validate every mapping:
   - host path must be absolute and non-empty
   - sandbox path must be absolute and non-empty when sandboxed
   - mode must be `ro` or `rw`, defaulting to `rw`
4. Build `OperationPathViews`.
5. Build `RequiredMount` entries for Shai from structured mappings.
6. Return replacements for all op-visible sentinels.

For `sandbox.type: none`, replacements use host paths and no mounts are needed.

For `sandbox.type: shai`, replacements use sandbox paths from
`inputs.sandbox.paths`, with defaults:

```go
map[string]string{
    contextual.OpWorkdirPathSentinel:    "/src",
    contextual.OpWorktreePathSentinel:   "/src/git",
    contextual.OpArtifactInboxSentinel:  "/src/inbox",
    contextual.OpArtifactOutboxSentinel: "/src/outbox",
}
```

Suggested error:

```text
op-visible path mapping failed: sandbox "shai" did not provide an outbox sandbox path
```

If `inputs.sandbox` contains any `__C2_SENTINEL_OP_*` value, fail before
execution. Sandbox selection and mount mapping cannot depend on the op-visible
path view they define.

Mount planning must normalize the resulting Shai mounts before invoking Docker.
Do not emit duplicate mounts with the same target, and do not emit identical
source/target/mode triples more than once. If Shai cannot support the required
parent/child mount layout, c2j should create a host-side staging layout and bind
that single staged root to `/src`.

### 8. Shared Process Execution API

Extract the current process-running behavior behind a shared command execution
base. `command_execution` should call it directly. Selector-backed extension
execution should resolve the selector, apply defaults and schemas, build the
same run request, and then call the same process runner.

Extension execution remains special only in these areas:

- selector resolution
- extension input/output schema validation
- JSON input/output envelope handling
- extension manifest/default processing

It should not own a separate path or working-directory model.

Update `command_execution`'s authored default working directory from the flat
host view to the op view:

```go
WorkingDirectory string `json:"working_directory" default:"{{ context.environment.op.worktree_path }}"`
```

For direct execution this still resolves to the host worktree path. For Shai it
resolves to `/src/git`, matching the prior Codex sandbox convention.

Extend the shared run request with required mounts and the command cwd:

```go
type RunRequest struct {
    WorkspaceRoot     string
    WorkingDir        string
    Shell             string
    Run               string
    Command           []string
    Env               map[string]string
    Stdin             []byte
    Sandbox           *SandboxInput
    RequiredMounts    []ops.RequiredMount
}
```

For Shai execution:

- Convert `RequiredMounts` into a Shai append resource set.
- Keep the existing Codex/Shai path targets such as `/src/inbox` and
  `/src/outbox`.
- Use `WorkingDir` as the command cwd. For Shai, this is the sandbox-visible
  command cwd, not a package-specific workdir.
- If current Shai does not honor `SandboxExec.Workdir`, c2j must wrap the
  command as `cd <working-dir> && exec ...` or upgrade Shai to honor it.

### 9. Runtime Validation

During selector-backed extension execution:

1. Sanitize the extension payload as today, removing reserved `sandbox`.
2. Validate the hydrated final payload against the extension input schema.
3. Read `OperationPathRuntime` from deps.
4. Pass runtime mounts to `ExecuteProcess`.

Validation must happen after op-sentinel hydration. The final payload reaching
the extension process should not contain any c2j sentinel strings.

During `command_execution`, there is no extension schema validation, but the
same hydrated command input and `OperationPathRuntime` must be passed to the
shared runner.

### 10. Preflight Schema Validation

Compiler preflight validation can see op-visible sentinels instead of final
runtime paths. For simple string fields this is fine:

```yaml
artifact_inbox_path:
  type: string
  default: "{{ context.environment.op.inbox }}"
```

Preflight sees:

```text
__C2_SENTINEL_OP_ARTIFACT_INBOX__
```

and the value still satisfies `type: string`.

But schemas with path-specific constraints can fail preflight even though runtime
hydration would pass:

```yaml
artifact_inbox_path:
  type: string
  pattern: "^/src/"
  default: "{{ context.environment.op.inbox }}"
```

For this case, preflight should either:

- skip full extension schema validation for fields containing op-visible
  sentinels and leave final validation to runtime, or
- treat op-visible sentinels as symbolic placeholders for string constraints.

Suggested code comment near preflight validation:

```go
// Preflight may see op-visible path sentinels instead of final host/sandbox
// paths. Runtime validation is authoritative for schemas that constrain the
// concrete path shape, because only the executor knows the selected sandbox.
```

Runtime validation remains authoritative because it sees `/src/inbox` for Shai
and the concrete host path for direct execution.

### 11. Command Execution Scope

`pkg/worker/commandop/command_execution.go` also calls
`pkg/ops/extensions.ExecuteProcess`.

The implementation should make this shared intentionally:

- move the reusable runner and sandbox path mapping out of extension-specific
  ownership, or make the package name reflect that it is shared process
  execution code;
- make `command_execution` and selector-backed extension execution use the same
  `SandboxInput`, path transformer, mount planner, and run request;
- keep selector-backed extension code as a command execution adapter with
  schema and selector handling.

## Tests

Add focused tests:

- Template context exposes:
  - `context.environment.host.inbox`
  - `context.environment.op.inbox`
  - equivalent fields for workdir, worktree, and outbox
- `context.environment.op.*` renders to op-visible sentinels before task
  dispatch.
- Existing flat fields still render to existing host sentinels.
- `sandbox.type: none` resolves op-visible sentinels to host paths.
- `sandbox.type: shai` resolves op-visible sentinels to `/src`,
  `/src/git`, `/src/inbox`, and `/src/outbox`.
- Shai path resolution creates mounts from structured host paths to structured
  sandbox paths.
- Shai mount planning deduplicates repeated mount targets before calling Docker.
- Extension `input_schema.default` can reference `context.environment.op.*`.
- Authored extension inputs and prompt strings can reference
  `context.environment.op.*`.
- Runtime validation sees hydrated op-visible path values.
- Preflight validation handles op-visible sentinel string placeholders.
- Sandbox config containing op-visible sentinels fails before process execution.
- Unknown sandbox config fields fail validation; there is no `inline_config`
  escape hatch.
- Missing `OperationPathTransformer` fails when op-visible sentinels remain.

Prefer unit tests around path mapping and sentinel hydration. Docker-backed Shai
integration tests can cover end-to-end behavior but should not be the only
coverage.

## Complexity

Recommended transformer-backed sentinel implementation with shared command
execution is medium-high complexity.

- Touches contextual/template surfaces.
- Adds new sentinels.
- Adds structured sandbox path config.
- Splits host sentinel hydration from op-visible sentinel hydration.
- Adds an optional op transformer interface.
- Adds sandbox mount mapping.
- Moves process execution/path mapping into shared command execution code.
- Updates selector-backed extension execution to use that shared base.

It is still preferable to compiler-time sandbox-dependent rendering because it
keeps sandbox path resolution at the process execution boundary.

## Encapsulation Impact

This approach adds a public template namespace:

```text
context.environment.op.*
context.environment.host.*
```

That is an additive recipe/template surface change.

The compiler does not need to understand Shai, Docker, or sandbox mount
internals. Sandbox-specific mapping stays in the op transformer and process
runner layer, where the actual process environment is created.

This does not require changing the fundamental compiler/template encapsulation
boundary. It does introduce a clearer execution boundary: extension execution
becomes an adapter over shared command execution instead of owning its own
process-running path model.

## Assumptions

- Windows host support is not in scope for sandboxed execution.
- Shai/Codex sandbox defaults keep `/src/inbox` and `/src/outbox`.
- Shai/Codex sandbox `worktree_path` defaults to `/src/git`, matching the prior
  Codex convention.
- There is no new reserved c2j sandbox path namespace.
- The structured sandbox config replaces arbitrary inline Shai config for this
  feature path.

## Remaining Questions

No blocking questions remain. Package placement for the shared process runner is
an implementation detail; the behavioral requirement is that command execution
and selector-backed extension execution use the same runner and sandbox path
mapping.
