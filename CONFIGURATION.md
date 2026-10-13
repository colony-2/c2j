# Configuration in task inputs

For recipe and extension-op examples, see the [secrets/config author guide](SECRETS_AND_CONFIG.md).

Use a whole-value template to read non-secret tenant configuration:

```yaml
sequence:
  - id: publish
    op: command_execution
    inputs:
      env:
        RELEASE_CHANNEL: ${{ context.config.release_channel }}
      run: ./publish
```

`context.config["build.channel"]` addresses names containing dots. Values retain
JSON types and must satisfy the receiving input schema. Missing names fail;
JSON `null` is a value, subject to that schema. Config may appear in public
outputs, logs and history. Use `context.secrets` for credentials.

Secret references support `context.secrets.?token.orValue(null)` to omit an
optional input or environment variable when the secret is missing or denied.
This does not change config semantics: config JSON null is a value, missing
config names still fail, and `orValue(null)` is not supported for config.
An optional input declaration alone does not make a supplied secret/config
reference optional. See the [author guide](SECRETS_AND_CONFIG.md#optional-secrets-and-public-fallbacks)
for secret omission, empty-string fallbacks, and required-input behavior.

c2j serializes references as
`{"$c2j_ref":{"kind":"config","name":"release_channel"}}`. Declared recipe
inputs also carry a `type` constraint when forwarded. These reserved objects
remain in submitted inputs and task hashes. The live task wrapper resolves a
separate input copy through `TaskContext.GetConfiguration`; cached tasks make
no reads. The outcome records the used names, JSON values and snapshot revision
under `configuration`, rather than storing the whole tenant snapshot.

JobDB owns caching behind that API. c2j has no configuration cache and does not
seed one from replayed outcomes. JobDB v0.0.32 caches the first successful
snapshot for the live job execution and starts fresh on resume. Successive tasks
use that snapshot through `TaskContext.GetConfiguration`. Within a task c2j
resolves all references from one API snapshot.

References can be forwarded through recipe, include and child inputs. Child
inputs marked for forwarding remain unresolved until the child executes.
References cannot be interpolated, transformed, inspected, or used directly in
`when`, state transitions, selectors, work enumeration or other recipe routing.
To branch on config, run an ordinary op that accepts the reference and returns
its resolved value. Route using that task's output; replay then preserves the
recorded decision even if config changes.

Extension manifest resolution, configuration resolution, tool installation and
secret preparation happen inside the consuming task, only on a cache miss.
Preparation currently shares the execution timeout. Successful outcomes include
`preparation_ms`, tool `setup` diagnostics and resolved `extension` metadata.
Failures returned by the worker attach preparation metadata as the outcome
artifact `_c2j/preparation.json`; a process crash or runtime timeout before the
worker returns may prevent that artifact from being recorded.

There are no separate config-recording, extension-resolution or setup tasks.
This execution format targets fresh databases; no old-history compatibility
path is provided. A future JobDB preparation phase can move preparation outside
the execution timeout without changing reference authoring.
