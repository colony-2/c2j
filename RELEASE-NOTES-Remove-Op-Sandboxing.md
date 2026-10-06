# Breaking change: operations use the worker environment

Commands and extensions now execute directly in the environment running the
c2j worker. c2j no longer creates Shai containers, discovers `.shai/config.yaml`,
or maps operation paths across a nested container boundary. Existing config
files may remain in repositories; c2j does not use them for operation execution.

Provision any required image, tools, credentials, environment, setup commands,
mounts, permissions, and isolation through the caller or external executor
before starting the worker. Removing a recipe field does not recreate that setup.
Execution requirements and JobDB allocation, lease, replay, completion, and
rescheduling behavior are unchanged.

## Recipe migration

Remove the former generic `sandbox` input, including `type: none`, and any
parameters/defaults that existed only to forward it. For example:

```yaml
# Before
id: build
op: command_execution
inputs:
  run: make
  sandbox: {type: shai}
```

```yaml
# After: the worker environment supplies the build tools and isolation.
id: build
op: command_execution
inputs:
  run: make
```

For selector shorthand, remove `inputs.sandbox`. For explicit
`extension_execution`, remove `inputs.inputs.sandbox`. Remove the recipe-test
`runtime.command_sandbox` option as well; passthrough commands use the worker
environment and disposable worktrees.

This release relies on ordinary input validation, without a special sandbox
rejection rule. Command compilation and dispatch reject `sandbox` as an unknown
input, even with `continue_on_error: true`. Extension inputs follow their
manifest schema: closed schemas reject undeclared fields; permissive schemas
can pass `sandbox` through as ordinary extension data. c2j never interprets or
strips it. This supersedes the feature request's dedicated migration-error and
universal rejection requirements. Unrelated application data and output fields
remain valid.

Stored recipes, inputs, and history are not rewritten. Finish jobs requiring
the removed runtime with a pinned older release, or resubmit deliberately
migrated recipes. Reconstruction of old recipes remains subject to current
input validation; there is no automatic migration or replay compatibility shim.
Coordinate c2ops and other recipe repository changes separately.

## Removed Go APIs

- `commandop.CommandExecutionInput.Sandbox`.
- `process.SandboxInput`, `SandboxPathConfig`, `SandboxPathMapping`,
  `SandboxTypeNone`, `SandboxTypeShai`, all `DefaultShai*` constants,
  `ParseSandboxInput`, `SandboxType`, and `TransformOperationPaths`.
- The corresponding sandbox aliases, constants, and parser in `extensions`,
  and `ResolvedOp.SanitizeInvocationInputs`.
- `process.RunRequest.ConfigFile`, `.Sandbox`, `.RequiredMounts`, and
  `.RequiredPorts` (also removed from the `extensions.RunRequest` alias).
- `ops.RequiredMount`, `RequiredPort`, `MountModeReadOnly`, `MountModeReadWrite`,
  `OperationPathViews`, `OperationPathRuntime`, `OperationPathRuntimeProvider`,
  `OperationPathTransformRequest`, `OperationPathTransformResult`, and
  `OperationPathTransformer`; `OpDependenciesBuilder.WithOperationPathRuntime`.
- `childbroker.Options.ContainerReachable` and
  `recipetest.RuntimeCase.CommandSandbox`.

Use `ops.OperationPaths`, `OperationPathProvider`, and
`OpDependenciesBuilder.WithOperationPaths` for invocation paths. Recipe
`context.environment.op.*` paths, inbox/outbox access, workspace snapshots,
extension assets, and immutable objects retain their contracts in the worker
filesystem.

The child broker uses loopback and a fresh port per invocation, retaining
lease-scoped submission, protected credentials, and per-invocation lifetime.
The worker and its subprocesses can run together in an externally provisioned
container. There is no container gateway translation or additional egress rule.

Shai and its unused transitive dependencies have been removed. No replacement
container launcher or runtime feature flag is provided.
