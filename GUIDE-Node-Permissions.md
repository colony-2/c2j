# Recipe node permissions

Secret inputs and delegated JobDB capabilities are separate authorities. Supplying
an input value never adds its secret name to the operation's delegated lease.

## Secret inputs: trust the root recipe repository

The trust root is the repository containing the root recipe being executed. It is
not the target cell's workspace repository. For a default build submission, for
example, the root recipe comes from `colony-2/recipes`.

| Where a secret reference originates | Driver may resolve the input without a grant? |
| --- | --- |
| Explicit input in the root recipe | Yes |
| Explicit input in another recipe from the root recipe repo | Yes |
| Default in an op from the root recipe repo | Yes |
| Explicit input in a recipe from another repo | No; the caller must authorize it at the include boundary, or the node needs an explicit grant within the caller's ceiling |
| Default in an op from another repo | No; the invoking scope needs an explicit grant for that secret |

A foreign include never establishes a new trust root. An op located beside a
foreign included recipe is still foreign to the original root recipe. Unknown
origins count as foreign; local symlinks are checked against the actual checkout.
Enclosing permission ceilings still apply to new secret references.

The driver resolves authorized references only when the consuming task runs.
Execution history contains the references, not their secret values. Replay uses
recorded task outcomes without fetching the secrets again.

### Input `foo`, delegated capability `bar`

This node belongs to the root recipe repo:

```yaml
id: separate-authorities
op: command_execution
perms:
  jobdb:
    secrets:
      resolve: [bar]
inputs:
  env:
    SERVICE_TOKEN: '${{ context.secrets.foo }}'
  run: ./worker
```

`SERVICE_TOKEN` receives the value of `foo`. The operation's delegated lease and
`c2j secret get` helper can retrieve `bar`, but cannot retrieve `foo`. Adding the
`bar` grant does not remove the trusted input's access to `foo`. The operation
already receives `foo` through its environment; this distinction controls further
secret retrieval, not what it can do with a supplied value.

Using `perms: {jobdb: {}}` would deny all delegated JobDB capabilities while still
allowing this trusted input reference to resolve. Omitting `perms` also permits
the input reference, with the normal delegated defaults described below.

### Explicit binding versus a foreign op default

Suppose an external op's schema declares a secret input named `api_key`, defaulting
to `${{ context.secrets.codex_api_key }}`. A trusted recipe can bind it explicitly:

```yaml
id: explicit-binding
op: git+https://github.com/example/tools.git//worker@main
inputs:
  api_key: '${{ context.secrets.codex_api_key }}'
```

The driver resolves this recipe-authored reference without adding `codex_api_key`
to the delegated lease. Use the actual input field defined by your op's schema.

To use the default supplied by that foreign op instead:

```yaml
id: foreign-default
op: git+https://github.com/example/tools.git//worker@main
perms:
  jobdb:
    secrets:
      resolve: [codex_api_key]
```

This explicit grant authorizes the foreign default and also permits runtime
retrieval through the delegated lease. Without it, the default is rejected before
secret retrieval or op installation. An optional/defaulted reference still needs
authorization. Overriding the default with an explicit trusted reference avoids
that grant; overriding it with `null` disables it only if the op schema permits.

## Include permissions authorize the included recipe

On an **include node**, `perms.jobdb` specifies authority entrusted to the included
recipe. Secret names listed there become available for its input references and
bound the grants it may request. They are **not** automatically added to its
nodes' delegated leases.

Caller in the root recipe repo:

```yaml
id: parent
sequence:
  - id: review
    include: git+https://github.com/example/reviews.git//review.yaml@main
    perms:
      jobdb:
        secrets:
          resolve: [foo, bar]
```

The foreign `review.yaml` can then contain:

```yaml
id: review
sequence:
  - id: input-only
    op: command_execution
    inputs:
      env:
        SERVICE_TOKEN: '${{ context.secrets.foo }}'
      run: ./review
  - id: input-and-lease
    op: command_execution
    perms:
      jobdb:
        secrets:
          resolve: [bar]
    inputs:
      env:
        SERVICE_TOKEN: '${{ context.secrets.foo }}'
      run: ./review
```

Both nodes receive `foo` in their input environment. The first node has no
model-secret retrieval capability. The second can retrieve `bar` through its
lease; its explicit grant does not replace the include's input authorization for
`foo`. Neither node can retrieve `foo` through its lease without its own grant.
A request for another secret exceeds the caller's ceiling and fails.

These input allowances follow same-repo includes within the authorized foreign
recipe. A further cross-repository include requires another explicit callsite
policy, and cannot enlarge the authority it received. Include allowances do not
implicitly authorize defaults from foreign ops: bind the secret explicitly in
the included recipe, or grant it on the invoking node.

### Passing a value is not granting a secret name

An include can receive a specific secret input without receiving permission to
look up that name elsewhere:

```yaml
id: parent
sequence:
  - include: git+https://github.com/example/reviews.git//consume.yaml@main
    perms:
      limit:
        jobdb: {}
    inputs:
      token: '${{ context.secrets.foo }}'
```

The foreign `consume.yaml` explicitly forwards that input:

```yaml
id: consume
input_schema:
  token:
    type: string
    secret: true
inputs:
  token: '${{ inputs.token }}'
sequence:
  - op: command_execution
    inputs:
      env:
        SERVICE_TOKEN: '${{ inputs.token }}'
      run: ./review
```

The consuming node receives the supplied value. Other nodes cannot independently
reference `${{ context.secrets.foo }}`, and no delegated lease gains `foo`.
Include inputs are evaluated in the caller's context before entering the included
recipe's policy. Passing an input does not add that name to the include's grants.

## Delegated defaults and ceilings

Every recipe uses restricted delegated defaults, including recipes with no
`perms`. Defaults allow job submission and the selected Git profile's concrete
secrets. Root and ordinary nodes use the configured RO profile; built-in `merge`
and `squashrebasemerge` use the RW default unless an enclosing `git_profile`
overrides it. `git_profile: null` removes automatic Git-secret contributions.
Resolving other secret inputs does not enlarge these defaults.

On ordinary nodes and scopes, an explicit `perms.jobdb` mapping replaces the
inherited **delegated grant**. Omitted entries deny operation access. If an explicit
grant also needs authenticated Git reads, include the actual RO secret name.
`jobdb: {}` denies all delegated capabilities. Omitted `jobdb` inherits; empty
`perms: {}` preserves the policy. Siblings recover the enclosing policy on scope
exit. These replacements do not replace trusted input-reference authorization.

`perms.limit.jobdb` sets a ceiling descendants cannot enlarge. Nested limits
intersect. A limit alone does not authorize a foreign input reference or create a
lease grant. The included recipe can request an explicit node grant within it.
Cross-repository includes must declare `perms.jobdb` or `perms.limit.jobdb` at the
callsite, even if empty. Inherited lease grants reset at that boundary. A selected
Git profile cannot override an explicit grant or ceiling.

Permission lists contain literal, exact secret names. Wildcards, expressions,
nulls, duplicate names, and unknown permission fields are rejected. Git profile
names belong in `git_profile`, not in JobDB grants.

Submission is a queue permission: `jobs.submit: true` permits submission within
the lease's tenant. Child jobs run under their own root recipe policy, with their
own trust root. They do not inherit the submitting job's grants or limits.
Chapters, configuration mutation, restarts, and other job lifecycle operations
remain unavailable through delegated defaults.

There is no `config.read` permission. Config references and `c2j config get` remain
driver-mediated, including inside foreign recipes and deny-all scopes. Reading a
Git profile definition does not itself authorize its secret references.

## Runtime integration

`pkg/permissions.Issuer` is the only credential creation boundary. Its JobDB
implementation calls the live `workflow.TaskContext.CreateDelegation` capability.
JobDB applies execution and parent bounds during local attenuation, without an
HTTP creation call. The operation never receives the owner lease. Child renewal
validates existing authority; it extends neither `notAfter` nor the parent lease.

When deadlines apply, the issued token has `notAfter` bounded by the engine's
effective task and enclosing deadlines, any parent delegation deadline, and any
shorter known operation timeout. It cannot gain time when refreshed. Recipe nodes
without an explicit or built-in timeout receive a one-hour scope timeout. Internal root-source
resolution adds no timeout; it inherits any applicable JobDB execution bounds.
When all bounds are absent, the restricted token lasts only while its exact
parent lease remains live. The engine may impose a shorter deadline. Extension
manifest timeouts narrow the operation and its helpers once resolved, before
installation and input secrets.
Preparatory source reads happen in the preceding node-bounded scope.

JobDB supplies task-scoped delegation.
The issuer preserves the live task context and passes only the resolved grant
and an optional stricter operation deadline. It does not reconstruct a budget
from the current clock or execution history. Helper/RPC request timeouts control
cancellation of that call, not credential lifetime. Credential creation is lazy;
a new operation limit obtains a narrower credential held by the helper.
The runtime wrapper forwards JobDB's optional `LeaseDelegator` capability and
preserves parent delegation metadata. Unsupported issuance or missing delegation
metadata fails without using an owner token as a fallback.
Embedded runtimes use the same per-call permission checks and engine cancellation
without minting a bearer token.

Embedders must install `executionruntime.Runtime.WrapTaskWorker`. Operation
workers reject invocations missing that guard, even when the task request omits
permission metadata. The guard applies restricted defaults to omitted metadata;
invalid policy versions fail. There is no unrestricted fallback.

These controls govern c2j's operation interfaces. Native Go ops and the driver
remain trusted. Isolating arbitrary subprocesses from the driver, owner-token
storage, ambient environment credentials, and other operations requires the
external executor's process/filesystem/network boundary. This feature does not
add an operating-system sandbox. A disclosed credential remains usable until its
hard deadline or parent lease loss; task completion does not revoke copies.
