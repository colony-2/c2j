# jq & JSON Helpers for Templates

New CEL helpers make it easy to query structured data and emit JSON from templates. These functions are available anywhere templates are evaluated (interpolation strings or single-expression mode).

## Functions
- `jq(value, expr)` / `value.jq(expr)` → dyn  
  - Parses and runs a jq expression against `value` using `gojq` (pure Go).
  - Returns the first jq result. Multiple results become a list. No results return `null` (resolved to Go `nil`).
  - If jq yields another iterator, it is drained into a list.
- `json_stringify(value)` → string  
  - JSON-encodes any marshalable value. Errors bubble up as CEL errors.
- `string(map|list)` JSON alias  
  - The `string()` overload JSON-encodes maps/lists instead of Go’s `map[...]` formatting for nicer interpolation output.

## jq Behavior
- Input: accepts maps, lists, primitives, or `null`. CEL-native types are unwrapped automatically.
- Output mapping:
  - 0 results → `null`
  - 1 result → that value (string/number/bool/map/list)
  - N>1 results → `[]interface{}` list
- Errors:
  - Parse: `jq: invalid expression: <error>`
  - Compile: `jq: compile failed: <error>`
  - Runtime: `jq: execution failed: <error>`

## JSON Helpers
- `json_stringify` and the `string(map|list)` overload both emit canonical JSON strings.
- JSON errors surface as `json_stringify: failed to encode JSON: <error>`.

## Examples
```yaml
# Pick fields
user_id: "{{ jq(inputs.payload, \".user.id\") }}"
user_name: "{{ jq(inputs.payload, \".user.name\") }}"

# Multiple results → list
tags: "{{ jq(inputs.payload, \".tags[]\") }}"

# Empty result → null
maybe_email: "{{ jq(inputs.payload, \".email // empty\") }}"

# JSON encode payload
payload_json: "{{ json_stringify(inputs.payload) }}"

# Interpolation uses JSON stringification for maps/lists
log: "Payload snapshot: {{ inputs.payload }}"
```

## Tips
- Prefer `jq` for concise field selection/filtering; it is compiled per call.
- Use `json_stringify` (or `string(map|list)`) when passing structured values to systems that expect JSON strings.
- When a jq expression is invalid, the template resolution fails fast with the jq error text.
