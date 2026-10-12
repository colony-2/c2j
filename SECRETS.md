# Job secrets

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
sequence:
  - op: command_execution
    inputs:
      env: {GH_TOKEN: '${{ inputs.token }}'}
      run: gh release create v1.0
```

Pass `${{ context.secrets.github_token }}` as that input. References can cross
include and child-job input boundaries; the receiving recipe validates its
schema. A child resolves under its own lease. Plaintext and defaults are rejected
for secret-marked inputs. Environment maps also accept ordinary nonsecret strings.
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
again against the real value before invoking the extension. Defaults are forbidden.
The explicit `extension_execution` wrapper also supports an `env` map; selectors
with secret manifest inputs should be invoked directly via `op: <selector>`.

Native Go ops opt in with `secret:"true"` on string input fields or `secret:"env"`
on environment maps. Child recipe ops forward references without materializing
them in the parent.

## Resolution, cache, and history

On a live task-result miss, the task wrapper resolves missing secrets before
invoking the op. Preparation and execution currently share the task timeout.
Retrieval creates no separate task or chapter; activity results include
`secret_setup_ms` and total `preparation_ms` when nonzero. Replay skips retrieval.
A future JobDB preparation phase can give preparation a separate timeout.

The first fetch batches required names with references visible in the already
loaded recipe, including future branches. No packages or includes are fetched
just to discover secrets. Batches contain at most 64 names. Later tasks reuse
values, concurrent fills coalesce, and newly encountered names are fetched later.
Missing future names do not fail the current task or become permanent misses.
Speculative batch failures fall back to required names alone.

The cache lasts for one local job execution, including task retries and normal
lease renewal. It is cleared when that execution exits, including cancellation,
handoff and lease loss. Resumed executions and child jobs get fresh caches.
Provider expiry timestamps are honored; rotation otherwise becomes visible at
the next fetch or execution. Cached task results and replay perform no reads.

History stores reserved reference objects containing names, never plaintext or
secret hashes. Rotating a value does not invalidate recorded task outcomes.
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
