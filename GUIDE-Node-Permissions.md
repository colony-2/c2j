# Recipe node permissions

Use `perms.jobdb` to control the JobDB authority available to a node. Git profiles
remain in the existing `git_profile` field; permission grants contain concrete
secret names, never Git profile names.

## Enable restricted defaults

For migration, recipes with no `perms` anywhere retain legacy behavior. Add
`perms: {}` at the root to enable restricted defaults throughout the recipe.
Any permission declaration in the expanded recipe enables the same contract.

```yaml
id: edit-and-publish
perms: {}
sequence:
  - id: edit
    op: command_execution
    perms:
      jobdb:
        secrets:
          resolve: [codex-key, repo-read-key]
        jobs:
          submit: true
    inputs:
      env:
        OPENAI_API_KEY: '${{ context.secrets["codex-key"] }}'
      run: codex exec 'Implement the requested change'
  - id: publish
    op: merge
  - id: inspect
    op: command_execution
    perms:
      jobdb: {}
    inputs:
      run: git status --short
```

Defaults allow job submission and the selected Git profile's concrete secrets.
The root and ordinary nodes use the configured RO profile. Built-in `merge` and
`squashrebasemerge` use the RW default unless an explicit enclosing `git_profile`
overrides it. `git_profile: null` removes the automatic Git secret contribution.
Other secrets require an explicit grant. Chapters, configuration mutation,
restarts, and job lifecycle operations are not available to the operation.
The trusted driver still prepares inputs and persists outcomes.

An explicit `jobdb` mapping replaces the entire inherited grant. Omitted entries
then deny access. Include the actual RO secret name if an explicit model-secret
grant also needs authenticated Git reads. `jobdb: {}` denies all JobDB operation
capabilities. Omitted `jobdb` inherits; empty `perms: {}` does not change a grant.
On scope exit, siblings get the enclosing policy again.

Permission names are literal, exact secret names. Wildcards, expressions, nulls,
and unknown permission fields are rejected. A secret reference in an input or
extension manifest does not authorize itself. Checks happen before resolving
secrets or consulting caches. The same grant controls materialized inputs,
`c2j secret get`, the Git credential helper, and submission through the child broker.

## Include limits

Use `perms.limit.jobdb` for a ceiling that descendants cannot enlarge. A ceiling
never grants a secret by itself. Nested limits intersect. Implicit defaults are
filtered by the ceiling; an explicit grant exceeding it fails.

Cross-repository includes require an explicit caller-authored ceiling:

```yaml
- include: git+https://github.com/example/recipes.git//review.yaml@main
  perms:
    limit:
      jobdb:
        secrets:
          resolve: [codex-key, repo-read-key]
        jobs:
          submit: false
```

At that boundary, inherited explicit grants reset; the included recipe receives
bounded defaults and may request explicit grants within the ceiling. Same-repo
includes inherit normally. Unknown repository origins and symlinks into another
checkout count as external. Nested foreign includes need their own limits.

Submission is a queue permission: `jobs.submit: true` allows job types and IDs
within the lease's tenant. Submitted jobs run under their own recipe policy and
do not inherit the submitter's grants or limits. There are no target selectors
in this first version.

## Configuration and credentials

There is no `config.read` permission. Direct config references and existing
`c2j config get` reads remain driver-mediated, including inside foreign includes
and deny-all scopes. Reading a Git profile definition does not authorize its
secret references.

Git RO/RW defaults resolve to the configured profile's `secret_ref` values in
the task's configuration snapshot. Secret values are fetched lazily and are not
serialized into recipes or policy metadata. A selected profile cannot override
an explicit grant or enclosing limit.

## Runtime integration

`pkg/permissions.Issuer` is the only credential creation boundary. Its JobDB
implementation uses local attenuation through `CreateDelegation`. JobDB handles
refresh; recipe authors do not configure transport refresh permissions.

Every issued token has `notAfter` bounded by the engine's effective task
and enclosing deadlines, any parent delegation deadline, and any shorter known
operation timeout. It cannot gain time when refreshed. Tasks without an explicit
or built-in timeout receive a one-hour scope timeout in restricted mode. The
engine may impose a shorter deadline. Extension manifest timeouts narrow the
operation and its helpers once resolved, before installation and input secrets.
Preparatory source reads happen in the preceding node-bounded scope.

The pinned JobDB main revision (`5a311bd4fff3`) has local attenuation but does
not yet expose the effective task deadline. [This upstream patch](JOBDB-TASK-DEADLINE.patch)
adds the accessor and its tests (`git apply --unidiff-zero JOBDB-TASK-DEADLINE.patch`
in the JobDB checkout). Remote restricted tasks remain unavailable
until that change is present in the dependency; c2j rejects remote
restricted execution when it cannot obtain it. It does not reconstruct a new
budget from the current clock or use an owner token as a fallback. Credential
creation is lazy; narrower calls obtain narrower credentials held by the helper.
Embedded runtimes use the same per-call permission checks and engine cancellation
without minting a bearer token.

Upgrade submitters and workers together. New c2j submissions use execution-demand
version 2, which older workers reject. Embedders must install
`executionruntime.Runtime.WrapTaskWorker`; restricted operation workers reject
invocations missing that guard. Execution-demand version 1 is still accepted; JobDB’s separate execution-history
version compatibility is governed by JobDB.

These controls govern c2j's operation interfaces. Native Go ops and the driver
remain trusted. Isolating arbitrary subprocesses from the driver, owner-token
storage, ambient environment credentials, and other operations requires the
external executor's process/filesystem/network boundary. This feature does not
add an operating-system sandbox. A disclosed credential remains usable until its
hard deadline or parent lease loss; task completion does not revoke copies.
