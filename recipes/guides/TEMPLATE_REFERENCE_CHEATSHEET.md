# Template Reference Cheatsheet

## Quick Reference
String fields support two expression systems:
- CEL expressions use `${{ ... }}`.
- Go string templates use `{{ ... }}`.

## Syntax Patterns

### CEL Expressions (`${{ ... }}`)
```yaml
# Multiple expressions in one string
message: "Hello ${{ inputs.name }}, your order ${{ inputs.order_id }} is ready"
url: "https://${{ inputs.domain }}/api/${{ inputs.version }}/users/${{ inputs.user_id }}"
log: "[${{ scope.timestamp }}] User ${{ inputs.user_id }} performed ${{ inputs.action }}"

# Mix static text with expressions
subject: "[${{ inputs.priority }}] Order ${{ inputs.order_id }} Update"
```

### CEL Single Expression (Returns Raw Type)
```yaml
# Returns actual data type (not stringified)
user_data: "${{ sequence.fetch.outputs.user }}"     # Returns map/object
count: "${{ sequence.calc.outputs.total }}"         # Returns number
is_valid: "${{ sequence.check.outputs.valid }}"     # Returns boolean
```

### Basic References
```yaml
# Input reference
value: "{{ inputs.field_name }}"

# Sequence node output (sibling within same sequence)
value: "{{ sequence.node_id.outputs.field_name }}"

# State output (previous states in state machine)
value: "{{ states.state_id.outputs.field_name }}"

# Scope metadata
value: "{{ scope.execution_id }}"
value: "{{ scope.timestamp }}"
```

### Nested Field Access
```yaml
# Deep object traversal
value: "${{ sequence.fetch.outputs.body.items[0].name }}"
value: "{{ states.validate.outputs.metadata.status }}"
```

### CEL Operations
```yaml
# String concatenation (old style - still works)
url: "${{ inputs.base_url + \"/api/\" + inputs.version }}"

# Arithmetic
count: "${{ sequence.calc.outputs.value * 2 + 10 }}"

# Type conversion (required for mixed types in CEL)
result: "${{ double(sequence.node.outputs.int_value) * 3.14 }}"
```

### When Conditions (Pure CEL - No Delimiters)
```yaml
# Boolean conditions in 'when' clauses - NO `${{ }}` or `{{ }}`
when: "sequence.validate.outputs.valid == true && sequence.transform.outputs.count > 0"
when: "inputs.retry_count < inputs.max_retries"
when: 'states.process.outputs.status == "success" || inputs.force == true'
```

### Go Templates (`{{ ... }}`)
```yaml
# Root context values are exposed as zero-arg functions:
# inputs, sequence, states, scope, context
message: "Cell {{ context.workflow.cell }} in project {{ context.workflow.project_id }}"
summary: "{{ inputs.title }} -> {{ sequence.build.outputs.status }}"

# Standalone simple paths can preserve scalar types
sleep_completed: "{{ sequence.sleep_step.outputs.completed }}"  # bool
exit_code: "{{ sequence.cmd.outputs.exit_code }}"               # number
```

## Scope Rules

### Sequences
- Can see: `inputs`, sibling nodes via `sequence.<node_id>`
- Cannot see: Parent sequence internals, future nodes

### State Machines
- States can see: `inputs`, completed states via `states.<state_id>`
- Transitions can see: Current state's sequence nodes if state is a sequence

### Nested Scopes
- Child sequences inherit parent's `inputs`
- Child states in state machine can see parent's completed `states`
- Scopes are isolated - cannot access siblings' internal nodes

## Retry/Loop Tracking
```yaml
# Latest attempt (default)
value: "{{ sequence.retry_node.outputs.result }}"

# Previous runs stored in .runs array
# Note: Accessing specific runs requires custom CEL functions
value: "{{ sequence.retry_node.runs }}"  # Returns array of previous attempts
```

## Common Patterns

### Sequence with Output Mapping
```yaml
sequence:
  - id: fetch
    op: http_get
    inputs:
      url: "{{ inputs.api_url }}"
      
  - id: process
    op: transform
    inputs:
      data: "{{ sequence.fetch.outputs.body }}"  # Reference previous node
      
outputs:
  result: "{{ sequence.process.outputs.transformed_data }}"
  status: "{{ sequence.fetch.outputs.status }}"
```

### State Machine Transitions
```yaml
states:
  initial:
    to: validate
    when: true
  
  validate:
    op: validator
    transitions:
      - to: process
        when: "states.validate.outputs.valid == true"  # Must use states context
      
  process:
    sequence:
      - id: transform
        op: transformer
        inputs:
          # Interpolation works in inputs
          message: "Processing item {{ inputs.item_id }} from batch {{ inputs.batch_id }}"
    transitions:
      - to: complete
        when: "sequence.transform.outputs.success == true"  # Access sequence nodes
```

## Key Differences
- **CEL Expressions**: `${{ ... }}` for CEL anywhere in strings
- **Go Templates**: `{{ ... }}` for Go template rendering in strings
- **Single CEL Expression**: One `${{ ... }}` alone returns raw type (map, number, bool)
- **Single Simple Go Path**: One `{{ root.path }}` can also return scalar types (bool/number/string)
- **When Conditions**: pure CEL expression string without delimiters
- **Quote Handling**: Both `"` and `'` quotes work inside expressions
- **No Dot Root**: Use `inputs.*`/`context.*` as functions, not `.inputs.*`/`.context.*`
- **Shared Functions**: `funcregistry.Add*` helpers expose functions to both CEL and Go templates.

## Type Conversion Functions
- `double()` - Convert to float64
- `int()` - Convert to int64
- `string()` - Convert to string
- `bool()` - Convert to boolean

## JSON Helpers
- `json_parse(str)` → map/list from a JSON string.
- `jq(value, expr)` → evaluate jq against any value (empty → null, multiple → list).
- `json_stringify(value)` → JSON string (CEL).
- `to_json(value)` → JSON string (Go templates).
- `string(map|list)` also JSON-encodes for interpolation.

```yaml
# Parse JSON string
config: "${{ json_parse(inputs.config_json) }}"

# Select fields with jq
user_id: "${{ jq(inputs.payload, '.user.id') }}"
tags: "${{ jq(inputs.payload, '.tags[]') }}"      # list when multiple
maybe_email: "${{ jq(inputs.payload, 'empty') }}" # null when empty

# Emit JSON
payload_json: "{{ inputs.payload | to_json }}"
log_line: "Snapshot: {{ inputs.payload }}"         # string(map) uses JSON
```

See the deeper guide: [jq & JSON Helpers](./JQ_JSON_TEMPLATE_GUIDE.md).

## Important Notes
1. **CEL in strings**: `"Text ${{ expr1 }} more ${{ expr2 }}"` → CEL substitution
2. **Go template in strings**: `"Text {{ inputs.name }}"` → Go template rendering
3. **Raw CEL types**: `"${{ single.expr }}"` → returns actual type (not stringified)
4. **Simple Go scalar coercion**: `"{{ sequence.step.outputs.flag }}"` can return `bool`/`number`/`string`.
5. **When Conditions**: no delimiters, pure CEL: `when: "inputs.count > 5"`
6. **`outputs` Scope**: In state `transitions.when`, `outputs.*` means current state outputs. Elsewhere, use `sequence.*` / `states.*` prefixes.
7. **Quote Escaping**: `${{ "string with }} inside" }}` and `${{ 'won''t fail' }}` both work
8. **Validation**: All expressions validated at compile time
