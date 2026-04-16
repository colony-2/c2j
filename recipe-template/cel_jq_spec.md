# CEL Custom Function Spec: jq

## Goal
Provide CEL functions to evaluate jq expressions and encode structured values as JSON, using `github.com/itchyny/gojq` (pure Go).

## Function Signatures
### `jq`
- Inputs:
  - `dyn` value (input data)
  - `string` jq expression
- Output: `dyn`
- Errors: invalid jq expression or execution errors should surface as CEL errors and fail resolution.

### `json_stringify`
- Inputs:
  - `dyn` value (input data)
- Output: `string`
- Errors: invalid input (non-JSON-marshalable) should surface as CEL errors and fail resolution.

## Behavior
- Evaluate the jq expression against the provided input value.
- The input value may be a map/list/primitive (CEL `dyn`).
- The function returns the first jq result by default.
- If the jq result set is empty, return `null` (CEL `nil`).
- If the jq result is another jq iterator, drain it and return a list of results.
- Strings returned by jq remain strings; numbers and booleans return as their native types.

## Suggested CEL Signature Variants
- `jq(input, expr)` (function style)
- Optional: `input.jq(expr)` (member style) for readability

## Integration Points
- Register the function during CEL env creation in `pkg/template/template_resolver.go` alongside other CEL functions.
- Use `cel.Function` + `cel.Overload` with `(dyn, string) -> dyn`.
- Implementation should:
  1. Parse the jq expression once per evaluation call using `gojq.Parse`.
  2. Compile the parsed expression with `gojq.Compile`.
  3. Run the program with `Run(value)`.
  4. Collect results and convert to CEL values with the active adapter (e.g. `env.CELTypeAdapter().NativeToValue(...)`).

## Input Conversion Rules
- CEL `dyn` value should be passed as-is to jq if it is composed of Go maps/slices/primitives.
- If the CEL value is a CEL-native type, unwrap to its Go value using `ref.Val.Value()` before passing to jq.
- If the input is `null`, jq should execute against `nil`.

## Output Mapping
- Single result -> return that value directly.
- Multiple results -> return a JSON array value (`[]interface{}`) with all results.
- No results -> return `nil`.

## Error Messages
- Parse error: `jq: invalid expression: <error>`
- Compile error: `jq: compile failed: <error>`
- Runtime error: `jq: execution failed: <error>`
- JSON encode error: `json_stringify: failed to encode JSON: <error>`

## Example Usage
```yaml
inputs:
  payload:
    user:
      id: 123
      name: "Ada"
sequence:
  - id: transform
    op: some_op
    inputs:
      user_id: "{{ jq(inputs.payload, '.user.id') }}"
      user_name: "{{ jq(inputs.payload, '.user.name') }}"
      payload_json: "{{ json_stringify(inputs.payload) }}"
```

## Testing
Add unit tests to `pkg/template/template_resolver_test.go` (or new file) that verify:
- Happy path: jq selects nested fields
- jq expression returning multiple results returns a list
- Empty result returns nil
- Invalid jq expression yields a CEL error
- Non-map input (string/number) still works for jq expressions like `.`
- json_stringify encodes map/list into JSON
- json_stringify errors on non-encodable values

## String Conversion Alias
For template ergonomics, treat `string(<non-primitive>)` as an alias of `json_stringify`:
- For map/list values, `string(value)` should return JSON (not Go's `map[...]` formatting).
- For primitives, preserve the existing string behavior.
- This can be achieved either by:
  1. Registering a custom `string(dyn)` overload that delegates to `json_stringify` for maps/lists, or
  2. Updating interpolation string conversion to JSON-encode map/list values, while recommending `json_stringify` in CEL expressions.
