# Template Resolution - Definitive Guide

Authoring templates follows the current resolver behavior (CEL + Go templates). Scopes constrain visibility; outputs must be explicitly produced to be referenced.

## Scope Model
- Hierarchy: `recipe` → `state_machine` → `state` → `sequence` → `op`.
- Each scope carries `inputs`, `sequence`, `states`, `scope`, `context`. There is no bare `outputs`.
- Child scopes inherit parent inputs/context; outputs are only visible within the same scope or upwards, never across sibling scopes.
- Completed executions are stored in `sequence.<id>` / `states.<id>` with both `outputs` and `artifacts`; retries/loops append to `.runs[]`.

## Resolution Rules
- CEL expressions use `${{ ... }}` in string fields.
- Go templates use `{{ ... }}` in string fields.
- A single `${{ ... }}` expression returns the raw CEL value (may be non-string).
- A single simple Go template path like `{{ sequence.step.outputs.flag }}` can also return scalar types (`bool`, `number`, `string`).
- Mixed CEL/text and mixed Go-template/text resolve to strings.
- Go template root values are exposed as zero-arg functions: `inputs`, `sequence`, `states`, `scope`, `context`.
- Custom functions registered through `funcregistry.AddZeroFunc*` / `AddUnaryFunc` / `AddBinaryFunc` are available in both CEL and Go templates.
- Dot-root access like `.inputs`/`.context` is not supported.
- When/conditions are pure CEL strings (no `${{ }}` and no `{{ }}`) and must evaluate to `bool`; empty or `"true"` is treated as `true`.
- Visibility:
  - A sequence sees its inputs, sibling node outputs via `sequence.<id>.outputs`, sibling artifacts via `sequence.<id>.artifacts`, and parent state-machine/state outputs via `states.<id>.outputs`.
  - An op sees the surrounding sequence/state-machine/state data.
  - A state sees completed states in the same state machine via `states.<id>.outputs` and `states.<id>.artifacts`.
  - Root/recipe cannot see inside sequences/states unless outputs are bubbled up. Sibling sequences in different states cannot see each other. Child sequences cannot see parent-sequence nodes.
- JSON helpers:
  - `jq(value, expr)` / `value.jq(expr)` for jq queries (empty→null, multi→list).
  - `json_stringify(value)` for CEL JSON-string output.
  - `to_json(value)` for Go-template JSON-string output (preferred in `{{ ... }}` strings, e.g. `{{ cells | to_json }}`).
  - `string(map|list)` also JSON-encodes during interpolation.
  - See [jq & JSON Helpers](./JQ_JSON_TEMPLATE_GUIDE.md) for details and examples.

## Examples
### Sequence inputs from prior nodes
```yaml
inputs:
  user_id: "{{ inputs.user_id }}"
  token: "{{ sequence.auth.outputs.token }}"
  profile_url: "https://{{ inputs.domain }}/api/{{ inputs.version }}/users/{{ inputs.user_id }}"
```

### Go template example
```yaml
inputs:
  summary: "Ticket {{ context.ticket.id }} in {{ context.workflow.cell }}"
```

### Sequence outputs mapping
```yaml
outputs:
  summary: "{{ sequence.transform.outputs.summary }}"
  total: "{{ sequence.fetch.outputs.body.total }}"
  first_item: "${{ sequence.transform.outputs.items[0] }}"
  payload_artifact: "${{ sequence.fetch.artifacts[\"payload.json\"] }}"
```

### State transition condition (pure CEL)
```yaml
transitions:
  - when: "sequence.transform.outputs.success == true && inputs.retry_count < 3"
    to: next
```

### State accessing previous state outputs
```yaml
inputs:
  validated: "{{ states.validate.outputs.valid }}"
  enriched: "{{ states.validate.outputs.metadata.source }}"
```

### Cross-run access (retries/loops)
```yaml
audit_message: "Last error: ${{ sequence.api_call.runs[0].outputs.error }}"
previous_artifact: "${{ sequence.api_call.runs[0].artifacts[\"stderr.txt\"] }}"
```

### Scoped isolation
- Invalid (will error): `outputs.value` (unqualified)
- Invalid: referencing `sequence.other_state_node` from a different state’s sequence
- Invalid: root trying to read `sequence.inner.outputs.x` without bubbling it up
