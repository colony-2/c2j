# Recipe schema versions and structured inputs

Set `schema_version: 2` at the recipe root to accept ordinary objects and arrays.
The existing `version` field identifies your recipe release; it does not select
c2j syntax. Omitted `schema_version` and explicit `schema_version: 1` retain the
original input contract, including `type: object` for checkpoint references.
Unknown schema versions fail instead of being interpreted as another version.

```yaml
schema_version: 2
id: annotated-review
version: "1.0.0"
input_schema:
  annotations:
    type: array
    default_value: []
    items:
      type: object
      additionalProperties: false
      properties:
        path:
          type: string
          required: true
        title:
          type: string
          default_value: Untitled
  labels:
    type: object
    default_value: {}
    additionalProperties:
      type: string
  session:
    type: checkpoint
    object_type: c2ops.codex.session/v1
inputs:
  annotations: ${{ inputs.annotations }}
  labels: ${{ inputs.labels }}
sequence: []
outputs:
  annotations: ${{ inputs.annotations }}
  labels: ${{ inputs.labels }}
```

Submit native YAML or JSON values through the existing input flags, includes,
or child recipe inputs. Whole-value `${{ ... }}` expressions retain these values;
JSON string encoding and decoding are unnecessary.

```yaml
annotations:
  - path: design.md
    title: Design and scope
labels:
  component: api
  priority: high
```

Version 2 supports `string`, `number`, `boolean`, `object`, `array`, `checkpoint`,
`artifact`, and `artifact_map`. `object_type` optionally constrains a checkpoint's
contract, such as `c2ops.codex.session/v1`. Checkpoint references and their stored
artifacts use the same wire format in both versions. Extension op manifests keep
their existing JSON Schema and `x-c2j-object-type` annotation.

An object's `properties` declares named fields. `additionalProperties` accepts
`false` to reject unknown keys, `true` to allow ordinary JSON values, or an input
schema to validate arbitrary string-keyed values. Its default is `true`.
Top-level recipe inputs still reject undeclared names. An array's `items` schema
applies to every element; omitted `items` allows ordinary JSON values. Framework
secret, configuration, and checkpoint references need explicitly typed fields.
Nested checkpoint and artifact fields use their existing validation rules.

`required: true` requires a field unless it has a non-null `default_value`.
Defaults apply only to missing fields, never to explicit null, false, zero,
empty strings, empty arrays, or empty objects. Declared types reject null;
`default_value: null` supplies no default, as in v1. A missing optional parent
object stays absent; use `default_value: {}` to create it and apply its child
field defaults. Defaults are validated recursively and copied per use, so
validating one invocation cannot change another invocation's defaults.
Errors identify nested locations, for example `annotations[2].path`.

Nested secret fields use `type: string, secret: true`. Submitted values must be
references, just as for top-level secrets. Configuration references remain
restricted to scalar fields. Neither feature is resolved during schema
validation; the consuming task retains its existing materialization behavior.

Each included or child recipe chooses its own schema version. A v1 recipe can
include a v2 recipe and vice versa. Caller version does not reinterpret the
callee's input contract. To migrate a recipe, add `schema_version: 2`, change
its checkpoint declarations from `type: object` to `type: checkpoint`, then add
structured input declarations as needed. Keep `object_type` constraints. Change
only the files you intend to migrate.

Compatibility is isolated in `pkg/recipe/input_schema_v1.go`, called from the
single version dispatcher in `pkg/recipe/schema_version.go`. It translates
legacy checkpoint declarations into the common input contract. Execution,
recursive validation, defaults, and dry-run input generation use that contract.
The adapter does not mutate authored schemas or persisted values.

Resolved recipes retain their original version and declarations. Expanded
include boundaries also persist the callee's schema version alongside its
input schema. Old boundaries lacking that field retain v1 behavior. Adding a
new field does not change the serialization or hash of an unversioned legacy
recipe. Replay uses the captured recipe, not the latest source or a new default
schema version. Both inline source history and source artifacts remain readable.

The compatibility tests include a completed job recorded by the unmodified
pre-versioning implementation, byte-for-byte legacy serialization and hash
fixtures, mixed-version includes, recovery with unavailable sources, child job
round trips, and both recipe test executors. Removing v1 support in the future
must be an explicit decision about old authoring **and old history**; keeping
old-job replay requires retaining its adapter.
