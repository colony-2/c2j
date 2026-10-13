# Secrets and configuration: author guide

Use `context.secrets` for credentials and `context.config` for non-secret tenant
settings. The runner retrieves values through JobDB when a task actually runs.
Submitting a recipe or replaying a completed task does not retrieve either.
The tenant's values must be configured in JobDB before execution; Pulse passes
execution access to runners rather than embedding credentials in recipe inputs.

## Choose the right kind of value

| | Secrets | Configuration |
|---|---|---|
| Reference | `${{ context.secrets.github_token }}` | `${{ context.config.release_channel }}` |
| Value type | String | JSON value matching the destination schema |
| Recipe input declaration | `type: string`, `secret: true` | Ordinary input schema; no special annotation |
| Extension input declaration | `type: string`, `x-secret: true` | Ordinary JSON Schema; no special annotation |
| Recorded values | References only; secret plaintext is excluded | Used values and snapshot revision are recorded with the task outcome |
| Outputs and logs | Do not emit credentials | Ordinary public data |
| Direct recipe routing | Forbidden | Forbidden; first return the value from a task |

## Read values in a recipe

Use a reference as the **entire input value**:

```yaml
id: publish
sequence:
  - id: release
    op: command_execution
    inputs:
      env:
        GH_TOKEN: '${{ context.secrets.github_token }}'
        RELEASE_CHANNEL: '${{ context.config.release_channel }}'
      run: ./scripts/publish
```

This assumes your workspace contains `scripts/publish` and the worker has the
required tools. The script reads the environment variables normally. Environment
values must resolve to strings; use a typed op input for a boolean or number.

Secret names start with a letter or underscore, contain letters, digits,
underscores or hyphens, and have at most 256 characters. For config names with
dots or other punctuation, use a literal string index:

```yaml
channel: '${{ context.config["release.channel"] }}'
```

References are opaque until task execution. Do not concatenate them, inspect
their contents, apply functions, or construct a name dynamically. These are
**unsupported**:

```text
Bearer ${{ context.secrets.github_token }}
${{ string(context.config.max_retries) }}
${{ context.secrets[inputs.secret_name] }}
```

To construct an authorization header, pass the secret into an allowed input and
build the header inside the op. Do not embed it in the recipe's `run` string.

## Declare reusable recipe inputs

A reusable recipe can accept references without choosing the tenant setting's
name itself:

```yaml
id: publish-package
input_schema:
  token:
    type: string
    secret: true
    required: true
  channel:
    type: string
    required: true
  dry_run:
    type: boolean
    default_value: false
inputs:
  token: '${{ inputs.token }}'
  channel: '${{ inputs.channel }}'
  dry_run: '${{ inputs.dry_run }}'
sequence:
  - id: publish
    op: ./ops/publish
    inputs:
      token: '${{ inputs.token }}'
      channel: '${{ inputs.channel }}'
      dry_run: '${{ inputs.dry_run }}'
outputs:
  published: '${{ sequence.publish.outputs.published }}'
```

The root `inputs` map forwards submitted recipe inputs into the sequence's
input scope. The local extension's manifest is shown below. A caller can supply:

```yaml
inputs:
  token: '${{ context.secrets.github_token }}'
  channel: '${{ context.config.release_channel }}'
  dry_run: '${{ context.config.dry_run }}'
```

Forward references through recipe/include/child inputs using `${{ inputs.token }}`
or `${{ inputs.channel }}`. A child job resolves its own references using its own
execution access. An include runs within the parent job.

Recipe config inputs support the ordinary `string`, `number` and `boolean`
schemas; forwarded references retain those type requirements. Artifact/object
inputs are separate mechanisms. Secret inputs require references, not literal
credentials. Secret-marked inputs may declare public string fallbacks; see below.

An ordinary `default_value` supplies an **omitted recipe input**. It does not
replace a missing config key when the caller supplied a config reference; use
`context.config.?name.orValue(...)` for that fallback.

## Missing configuration and fallbacks

An absent config name resolves to JSON null. To use a default instead, supply a
whole-value optional lookup:

```yaml
channel: '${{ context.config.?release_channel.orValue("stable") }}'
dry_run: '${{ context.config.?dry_run.orValue(false) }}'
retries: '${{ context.config.?max_retries.orValue(3) }}'
optional: '${{ context.config.?setting.orValue(null) }}'
```

For names containing punctuation, use
`${{ context.config[?"build.channel"].orValue("stable") }}`. Fallbacks accept
literal strings (single or double quotes), numbers, booleans, or null, not other
expressions. They remain deferred references until the consuming task runs.
The same syntax works in recipe inputs and inputs passed to extension ops.

Only an absent name selects the fallback. Existing null, false, zero and empty
strings remain unchanged. Provider errors still fail the task. Resolved values
must satisfy the receiving schema, so a string-only input needs a string
fallback when config may be absent. JSON Schema `type: [string, "null"]` permits
null for an extension input.

Config null keeps the field present, including with `orValue(null)`; it does not
omit the field as a secret null fallback does. Two inputs may choose different
fallbacks for the same missing name. The task records absent names in its
`configuration.missing` metadata; its input references retain their fallbacks.
These references still cannot be used directly for routing decisions.

## Declare extension-op inputs

For a local `ops/publish/op.yaml`, declare secret string properties using
`x-secret: true`. Configuration uses ordinary properties:

```yaml
name: publish
input_schema:
  type: object
  required: [token, channel]
  properties:
    token:
      type: string
      x-secret: true
      minLength: 1
    channel:
      type: string
    dry_run:
      type: boolean
      default: false
output_schema:
  type: object
  required: [published]
  properties:
    published:
      type: boolean
run: python3 main.py
```

This assumes Python and your `main.py` implementation are available. The process
receives resolved values as its usual JSON input on stdin, for example:

```json
{"token":"<resolved credential>","channel":"stable","dry_run":false}
```

Read that JSON and pass the credential to the client library making the request.
Return only public results, such as `{"output":{"published":true}}`. Do not log
the input object wholesale. Validate and use values normally: no JobDB calls or
reference decoding are needed in your op.

The same `input_schema` annotations apply to packaged Nix ops. Use the Nix
manifest's `command` instead of `run`; see [the extension-op guide](EXTENSION_OPS.md).
Invoke selectors directly as `op: <selector>` so c2j prepares the manifest and
package before execution.

Secret annotations are supported on string schemas under `properties`, `items`
and `additionalProperties`, including nesting. They are not supported through
`$ref`, `$defs`, schema combinators or `patternProperties`. Output schemas cannot
be secret-marked. String constraints such as `minLength` are checked against the
resolved value before invoking the op.

An extension's ordinary `default` fills an omitted input, not an unavailable
config reference. A secret-marked property may declare a public string fallback.

## Make routing decisions from recorded outputs

Neither namespace is available directly in `when`, state transitions, selectors,
work enumeration or other recipe routing. Forwarding a reference through an
input does not remove this restriction.

For config-driven routing, explicitly return the setting from a task first:

```yaml
id: publish-stable-only
sequence:
  - id: channel
    op: command_execution
    inputs:
      env:
        RELEASE_CHANNEL: '${{ context.config.release_channel }}'
      run: printf '%s' "$RELEASE_CHANNEL"
  - id: publish
    when: sequence.channel.outputs.stdout == "stable"
    op: command_execution
    inputs:
      env:
        GH_TOKEN: '${{ context.secrets.github_token }}'
      run: ./scripts/publish
```

The first task's recorded output determines routing on replay. If config changes
after suspension, completed tasks retain their outputs; unfinished tasks resolve
their references through the resumed execution. **Do not echo a secret** to make
it available for routing.

## Execution, failures and visibility

Values resolve inside the consuming task, before its op body. Preparation and
execution currently share the task timeout. Cached tasks skip retrieval and
extension/tool preparation; changing a setting does not invalidate an existing
task result.

c2j requests the current task's secret names through JobDB, in batches of up to
64, and clears its transient input values after each task attempt. It does not
prefetch future tasks' secrets or cache values across tasks. Only execution-wide
redaction tracking remains in c2j, to protect later logs/errors/outputs.

JobDB v0.0.32 caches secrets and configuration for the live job execution.
Available secrets and missing/denied names are cached; known omissions are not
retried within that execution. Requesting a new name refreshes previously
available secrets together. Expired secrets fail resolution. Configuration uses
the first successful snapshot for the execution. Resumed executions and child
jobs get fresh caches, and replayed outcomes never seed them.

| Situation | Current behavior |
|---|---|
| Secret missing or denied | Uses a string fallback, omits the field for `orValue(null)`, or fails without a fallback |
| Secret provider failure | Task fails: `secret resolution unavailable` |
| Empty secret | Valid string, unless the receiving schema rejects it |
| Config key missing | Resolves to null, or the explicit `orValue(...)` fallback; receiving schema still applies |
| Config value is JSON `null` | Preserved as a value, subject to the receiving schema |
| Config read failure or invalid input type | Task fails before the op runs |

Normal retry and `catch` handling apply. Unhandled failures fail the job; there
is no automatic pause waiting for a secret to be supplied. Secret error messages
currently omit the secret name and underlying provider reason.

Config values are public and are recorded under the task outcome's
`configuration` metadata, alongside the revision used. Secrets remain references
in durable inputs. c2j masks known secret values in its default logs and returned
errors, including extension stderr, and rejects structured outputs containing
them. Ops must not write credentials into artifacts or workspaces, or encode them
into outputs; masking does not protect arbitrary files or transformed values.

Offline recipe tests can use mocked op results without retrieving secrets or
config. Passthrough requiring either needs a lease-backed job execution.

## Optional secrets and public fallbacks

Use a whole-value optional reference with a literal string fallback:

```yaml
env:
  GH_TOKEN: '${{ context.secrets.?github_token.orValue("") }}'
```

The argument to `orValue` must be a CEL string literal (single or double quotes)
or `null`. Variables, functions, concatenation, and secret-to-secret fallbacks
are not supported. The reference remains opaque; it cannot be used in routing or outputs.

To leave a secret unset when it is missing or denied, use `orValue(null)`:

```yaml
env:
  GH_TOKEN: '${{ context.secrets.?github_token.orValue(null) }}'
```

Unlike `orValue("")`, this **omits the field** instead of supplying an empty
string or JSON null. For environment maps, c2j also removes that variable from
the child process's inherited environment. It does not modify the runner's
environment. Protected runner metadata keeps its normal precedence.

Secret inputs are optional unless declared required. For example, an extension
can accept the reference above through this optional input:

```yaml
input_schema:
  type: object
  properties:
    token: {type: string, x-secret: true}
```

| Input/reference | If the secret is unavailable |
|---|---|
| Optional input omitted by the caller | No lookup; schema defaults, if any, still apply |
| `context.secrets.token` | Fails unless a string schema default is provided |
| `context.secrets.?token.orValue("")` | Supplies an empty string |
| `context.secrets.?token.orValue(null)` | Omits the field; fails if the receiving input is required |

A recipe uses `required: true`; an extension lists the property in JSON Schema's
`required` array. Those constraints still apply to null fallbacks, including
recipe inputs forwarded to another op. Available secrets, including empty
strings, remain present and are validated normally. Explicit `orValue(null)`
takes precedence over a string schema default; the default is not reapplied
after omission. Omission works for object fields and environment entries,
not individual array elements.

Schema defaults remain **string-only**. Extension `default: null` is rejected;
recipe `default_value: null` means no default. Use `orValue(null)` on the
reference to request omission. This behavior is specific to secrets: config
JSON null remains a normal value, and missing config names resolve to null or
their explicit fallback without omitting the field.

A reusable recipe can supply its own fallback for a secret input:

```yaml
input_schema:
  token:
    type: string
    secret: true
    default_value: ""
```

Extension manifests use the ordinary JSON Schema `default` annotation:

```yaml
input_schema:
  type: object
  properties:
    token:
      type: string
      x-secret: true
      default: ""
```

Resolution uses the available secret first, then the reference's explicit
fallback (including null omission), then the receiving schema's default. Without any fallback, a missing
or denied secret fails the task. An available empty secret stays empty.
Fallbacks attached by a recipe survive forwarding; a downstream schema cannot
replace an already attached fallback. Extension string constraints still apply
to the value actually passed to the op, including fallbacks.

If a secret input is omitted entirely, its schema default supplies the value
without a provider read. Without a default, ordinary required-input rules apply.
An explicit null or plaintext input is still rejected.

**Defaults are public:** they can appear in recipe/manifests and durable
reference metadata. Never put a credential in a fallback. Defaults are literal
strings, not evaluated templates, and are never stored as a secret in the
execution cache. Two inputs referencing the same missing name can therefore
use different defaults. JobDB caches missing/denied names for the execution;
a newly supplied secret becomes visible after a fresh execution starts.

Only a successful provider response that omits a name permits its fallback.
Provider outages, an unconfigured provider, cancellation, timeout, and lease
loss still fail execution.
