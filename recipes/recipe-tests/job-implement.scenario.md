# job-implement test suite

```yaml
cases:
  - id: ts-011-continue-session-path
    type: recipe_case
    inputs:
      title: Continue work
      description: Resume prior coding session
      session_id: sid-existing
      include_prior_summary: false
    mocks:
      ops:
        - match:
            node_path: job-implement/continue_session/codex.exec
          behavior:
            mode: return
            outputs:
              status: completed
              sessionId: sid-existing
              assistantSummary: Continued implementation session
              incompleteReason: ""
              incompleteCategory: ""
              pendingDependencies: []
    assertions:
      - type: output_equals
        path: session_id
        value: sid-existing
      - type: output_equals
        path: status
        value: completed

  - id: ts-012-new-session-path
    type: recipe_case
    inputs:
      title: Start fresh
      description: New implementation session
      include_prior_summary: true
      prior_session_summary: Previous run summary
    mocks:
      ops:
        - match:
            node_path: job-implement/new_session/codex.exec
          behavior:
            mode: return
            outputs:
              status: completed
              sessionId: sid-new
              assistantSummary: Started new session with provided context
              incompleteReason: ""
              incompleteCategory: ""
              pendingDependencies: []
    assertions:
      - type: output_equals
        path: session_id
        value: sid-new
      - type: output_equals
        path: status
        value: completed

  - id: ts-013-assistant-summary-output
    type: recipe_case
    inputs:
      title: Summary output
      description: Ensure summary output exists
    mocks:
      ops:
        - match:
            node_path: job-implement/new_session/codex.exec
          behavior:
            mode: return
            outputs:
              status: completed
              sessionId: sid-summary
              assistantSummary: Non-empty summary for reviewer context
              incompleteReason: ""
              incompleteCategory: ""
              pendingDependencies: []
    assertions:
      - type: output_equals
        path: assistant_summary
        value: Non-empty summary for reviewer context
```
