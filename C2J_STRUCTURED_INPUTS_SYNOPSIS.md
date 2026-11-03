# Structured recipe inputs in c2j schema version 2

Recipes can opt into `schema_version: 2` to accept ordinary objects and arrays
directly. Omitted versions and explicit version 1 retain the legacy schema,
where `object` denotes a checkpoint. Version 2 uses `checkpoint` for that value.
The recipe release `version` is independent of its `schema_version`.

For example:

```yaml
schema_version: 2
input_schema:
  annotations:
    type: array
    default_value: []
    items:
      type: object
      properties:
        path:
          type: string
          required: true
        title:
          type: string
          required: true
  labels:
    type: object
    default_value: {}
    additionalProperties:
      type: string
```

`properties` describes named fields; `additionalProperties` describes values
under arbitrary string keys, or accepts a boolean to allow or reject them.

Callers would supply ordinary YAML or JSON values:

```yaml
inputs:
  annotations:
    - path: design.md
      title: Design and scope
  labels:
    component: api
    priority: high
```

Recipes could then read or forward `${{ inputs.annotations }}` directly, without
`json_parse` or `json_stringify`.

Nested validation reports paths such as `annotations[2].path`, applies declared
defaults, and preserves structures through includes, child jobs, persistence,
and recipe tests. Both versions share a validator through a narrow legacy
adapter. Persisted source and expanded includes retain their declaring schema
version so replay does not reinterpret old checkpoint declarations.

See [Recipe schema versions and structured inputs](GUIDE-Recipe-Schema-Versions.md)
for exact semantics, migration, and replay compatibility.
