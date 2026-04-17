# Template Reference Cheatsheet

## Quick Reference
Templates support **string interpolation** with multiple `{{ }}` expressions OR single CEL expressions.

## Syntax Patterns

### String Interpolation (NEW - Preferred)
```yaml
# Multiple expressions in one string
message: "Hello {{ inputs.name }}, your order {{ inputs.order_id }} is ready"
url: "https://{{ inputs.domain }}/api/{{ inputs.version }}/users/{{ inputs.user_id }}"
log: "[{{ scope.timestamp }}] User {{ inputs.user_id }} performed {{ inputs.action }}"

# Mix static text with expressions
subject: "[{{ inputs.priority }}] Order {{ inputs.order_id }} Update"
```

### Single Expressions (Returns Raw Type)
```yaml
# Returns actual data type (not stringified)
user_data: "{{ sequence.fetch.outputs.user }}"     # Returns map/object
count: "{{ sequence.calc.outputs.total }}"         # Returns number
is_valid: "{{ sequence.check.outputs.valid }}"     # Returns boolean
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
value: "{{ sequence.fetch.outputs.body.items[0].name }}"
value: "{{ states.validate.outputs.metadata.status }}"
```

### CEL Operations (Still Supported)
```yaml
# String concatenation (old style - still works)
url: "{{ inputs.base_url + \"/api/\" + inputs.version }}"

# Arithmetic
count: "{{ sequence.calc.outputs.value * 2 + 10 }}"

# Type conversion (required for mixed types in CEL)
result: "{{ double(sequence.node.outputs.int_value) * 3.14 }}"
```

### When Conditions (Pure CEL - No Interpolation)
```yaml
# Boolean conditions in 'when' clauses - NO {{ }} markers
when: "sequence.validate.outputs.valid == true && sequence.transform.outputs.count > 0"
when: "inputs.retry_count < inputs.max_retries"
when: 'states.process.outputs.status == "success" || inputs.force == true'
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
  initial: validate
  
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
- **Interpolation**: Multiple `{{ }}` in one string creates interpolated string
- **Single Expression**: One `{{ }}` alone returns raw type (map, number, bool)
- **When Conditions**: Pure CEL without `{{ }}` markers
- **Quote Handling**: Both `"` and `'` quotes work inside expressions
- **No Leading Dots**: Use `inputs.field` not `.inputs.field`

## Type Conversion Functions
- `double()` - Convert to float64
- `int()` - Convert to int64
- `string()` - Convert to string
- `bool()` - Convert to boolean

## Important Notes
1. **String Interpolation**: `"Text {{ expr1 }} more {{ expr2 }}"` → interpolated string
2. **Raw Types**: `"{{ single.expr }}"` → returns actual type (not stringified)
3. **When Conditions**: No `{{ }}`, pure CEL: `when: "inputs.count > 5"`
4. **No Direct Outputs**: Always use context prefix (`sequence.`, `states.`) - never `outputs.` alone
5. **Quote Escaping**: `{{ "string with }} inside" }}` and `{{ 'won''t fail' }}` both work
6. **Validation**: All expressions validated at compile time