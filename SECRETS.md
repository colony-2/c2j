# Job secrets

For recipe and extension-op examples, see the [secrets/config author guide](SECRETS_AND_CONFIG.md).

Recipes reference names; runners fetch values from JobDB using their current
execution lease. Submitting a job and replaying its history require no secret
reads. Pulse uses its existing lease handoff without transporting secret values.

```yaml
id: publish
sequence:
  - id: release
    op: command_execution
    inputs:
      env:
        GH_TOKEN: ${{ context.secrets.github_token }}
      run: gh release create v1.0
```

Only whole-value references are supported. Do not interpolate secrets into `run`,
paths, conditions, IDs, outputs, or CEL/Go-template transformations. Secret names
use letters, digits, underscores and hyphens, start with a letter or underscore,
and are at most 256 characters. There is no secret enumeration or dynamic lookup.

## Inputs and forwarding

A recipe accepting a secret declares a string input with `secret: true`:

```yaml
id: publish
input_schema:
  token: {type: string, secret: true, required: true}
inputs:
  token: '${{ inputs.token }}'
sequence:
  - op: command_execution
    inputs:
      env: {GH_TOKEN: '${{ inputs.token }}'}
      run: gh release create v1.0
```

Pass `${{ context.secrets.github_token }}` as that input. References can cross
include and child-job input boundaries; the receiving recipe validates its
schema. A child resolves under its own lease. Plaintext inputs are rejected;
public string defaults are supported for secret-marked inputs. Environment maps also accept ordinary nonsecret strings.
An empty secret value is valid; a missing or denied required name fails setup.
Values retain trailing newlines.

Extension manifests annotate secret string inputs with `x-secret: true`:

```yaml
input_schema:
  type: object
  required: [token]
  properties:
    token: {type: string, x-secret: true}
```

The extension receives the materialized input through its usual JSON stdin.
Annotations are supported on direct/nested `properties`, `items`, and
`additionalProperties` string schemas. Permission is not inferred through
`$ref`, schema combinators, or `patternProperties`. String constraints are checked
again against the materialized value before invoking the extension, including fallbacks.
The explicit `extension_execution` wrapper also supports an `env` map; selectors
with secret manifest inputs should be invoked directly via `op: <selector>`.

Native Go ops opt in with `secret:"true"` on string input fields or `secret:"env"`
on environment maps. Child recipe ops forward references without materializing
them in the parent.

## Optional secrets

Use `${{ context.secrets.?github_token.orValue("") }}` for an explicit fallback.
A literal CEL string supplies a value; `orValue(null)` requests omission. Recipes may declare `default_value: ""`
on a secret input; extensions may declare `default: ""` beside `x-secret: true`.

An available secret wins, including an empty string. Otherwise the reference's
fallback wins over the receiving schema's default; without either, setup fails.
Defaults survive forwarding and remain specific to each input binding. An omitted
secret input with a schema default requires no lookup. Defaults are public,
recordable strings and are never cached or masked as credentials.

Use `${{ context.secrets.?github_token.orValue(null) }}` to leave an optional
secret unset. A missing or denied secret removes the input field or environment
entry rather than supplying JSON null or `""`. Process environment omission also
suppresses inherited values; protected runner metadata retains precedence.
Available secrets, including empty strings, remain present.

Optional inputs can be omitted by callers, but supplying a plain secret reference
still requires resolution unless a fallback exists. Recipe `required: true`,
extension JSON Schema `required`, and native input constraints remain enforced
after omission. Explicit null fallbacks override string schema defaults.
Null omission is supported for object fields/environment entries, not individual
array elements. Schema defaults stay string-only: extension `default: null`
is rejected and recipe `default_value: null` means no default.

A missing/denied name in a successful provider response permits fallback.
Provider failures (including no configured provider), cancellation, timeouts,
and lease loss still fail. See the [author guide](SECRETS_AND_CONFIG.md#optional-secrets-and-public-fallbacks)
for examples and forwarding behavior.

## Resolution, cache, and history

On a live task-result miss, the task wrapper resolves missing secrets before
invoking the op. Preparation and execution currently share the task timeout.
Retrieval creates no separate task or chapter; activity results include
`secret_setup_ms` and total `preparation_ms` when nonzero. Replay skips retrieval.
A future JobDB preparation phase can give preparation a separate timeout.

Each live task requests its own referenced names through
`TaskContext.ResolveSecrets`, deduplicated into batches of at most 64 names.
c2j does not prefetch future tasks' secrets or cache resolved values across tasks.
Its transient input values are cleared when the task attempt returns, including
failures. Fallbacks remain local to each input binding.

JobDB v0.0.32 owns caching for the live job execution. It caches both available
values and missing/denied names. Requesting a new name refreshes previously
available names together; known omissions are not retried within that execution.
Expired values fail resolution. Resumed executions and child jobs use fresh
JobDB caches. Cached task results and replay perform no reads.

c2j retains an in-memory redaction set for the local execution so later tasks'
logs, errors, and outputs cannot disclose a secret fetched by an earlier task.
This set cannot resolve names or supply input values and is cleared when the
execution exits.

History stores reserved reference objects containing names and any public
fallbacks, never secret plaintext or secret hashes. Rotating a value does not invalidate recorded task outcomes.
Values are registered for masking before invocation. c2j masks known values in
its default logs and returned errors, including extension stderr, and rejects
structured op outputs containing them. Ops must not write credentials into
artifacts/workspaces, bypass c2j logging, or encode them into outputs: masking is
not isolation from untrusted code.

Configure the server-side provider using JobDB's
[secrets guide](https://github.com/colony-2/jobdb/blob/v0.0.30/docs/GUIDE-SQLITE-SECRETS.md).
The SQLite development provider grants tenant-wide access to valid leases;
per-job authorization belongs in a custom JobDB provider.

Use `NewRecipeWorkerWithOptions` to wire the session lifecycle automatically.
Hosts registering workers separately must pass the activity registry's
`SecretSessions` to `RecipeJobWorkerOptions`. Offline recipe tests can mock ops
using references; secret-consuming passthrough requires a lease-backed job run.
