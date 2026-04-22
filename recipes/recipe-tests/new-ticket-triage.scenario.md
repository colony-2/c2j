# new-ticket-triage test suite

```yaml
cases:
  - id: ts-001-appropriate-cell
    type: recipe_case
    inputs:
      title: Add retry handling
      description: Improve resilience
    mocks:
      ops:
        - match:
            node_path: new-ticket-triage/assess_cell
          behavior:
            mode: return
            outputs:
              status: completed
              assistantSummary: '{"cell_is_appropriate":true,"recommended_cell":"core","rationale":"Best match"}'
            artifacts:
              triage/latest-status.json: '{"cell_is_appropriate":true,"recommended_cell":"core","rationale":"Best match"}' 
    assertions:
      - type: output_equals
        path: cell_is_appropriate
        value: true
      - type: output_equals
        path: recommended_cell
        value: core

  - id: ts-002-out-of-cell-decision
    type: recipe_case
    inputs:
      title: Build admin dashboard
      description: UI-heavy task likely outside current cell
    mocks:
      ops:
        - match:
            node_path: new-ticket-triage/assess_cell
          behavior:
            mode: return
            outputs:
              status: completed
              assistantSummary: '{"cell_is_appropriate":false,"recommended_cell":"frontend","rationale":"UI scope"}'
            artifacts:
              triage/latest-status.json: '{"cell_is_appropriate":false,"recommended_cell":"frontend","rationale":"UI scope"}' 
    assertions:
      - type: output_equals
        path: cell_is_appropriate
        value: false
      - type: output_equals
        path: recommended_cell
        value: frontend

  - id: ts-003-invalid-recommended-cell-fallback-check
    type: recipe_case
    inputs:
      title: Unknown target
      description: Should force fallback validation
    mocks:
      ops:
        - match:
            node_path: new-ticket-triage/assess_cell
          behavior:
            mode: return
            outputs:
              status: completed
              assistantSummary: '{"cell_is_appropriate":false,"recommended_cell":"__not_a_real_cell__","rationale":"invalid"}'
            artifacts:
              triage/latest-status.json: '{"cell_is_appropriate":false,"recommended_cell":"__not_a_real_cell__","rationale":"invalid"}' 
    assertions:
      - type: output_equals
        path: recommended_cell_is_valid
        value: false
      - type: output_equals
        path: recommended_cell
        value: __not_a_real_cell__

  - id: ts-004-triage-artifact-emitted
    type: recipe_case
    inputs:
      title: Emit triage artifact
      description: Ensure triage json is persisted
    mocks:
      ops:
        - match:
            node_path: new-ticket-triage/assess_cell
          behavior:
            mode: return
            outputs:
              status: completed
              assistantSummary: '{"cell_is_appropriate":true,"recommended_cell":"core","rationale":"artifact check"}'
            artifacts:
              triage/latest-status.json: '{"cell_is_appropriate":true,"recommended_cell":"core","rationale":"artifact check"}' 
    assertions:
      - type: artifact_exists
        path: triage/latest-status.json
```
