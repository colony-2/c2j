# new-ticket-implementation-planning test suite

```yaml
cases:
  - id: ts-028-implementation-plan-artifacts
    type: recipe_case
    inputs:
      title: Add auditing
      description: Plan implementation sequencing
    mocks:
      ops:
        - match:
            node_path: new-ticket-implementation-planning/build_plan
          behavior:
            mode: return
            outputs:
              status: completed
              assistantSummary: '{"summary":"Audit plan","requires_dependency_jobs":false,"dependency_order":["REQ-1"],"dependency_job_specs":[],"local_steps":["Implement audit hooks"],"notes_for_user_review":"Plan is local-only"}'
            artifacts:
              implementation/plan.json: '{"summary":"Audit plan","requires_dependency_jobs":false,"dependency_order":["REQ-1"],"dependency_job_specs":[],"local_steps":["Implement audit hooks"],"notes_for_user_review":"Plan is local-only"}'
              implementation/index.md: '# Implementation plan\n- local-only'
              implementation/REQ-1.md: '# REQ-1\nNo dependency job required' 
        - match:
            node_path: new-ticket-implementation-planning/compat_review
          behavior:
            mode: return
            outputs:
              status: completed
              assistantSummary: '{"ok":true,"feedback":"Compatible","blocking_issues":[]}'
            artifacts:
              implementation/compat-review.json: '{"ok":true,"feedback":"Compatible","blocking_issues":[]}'
              implementation/compat-review.md: '# Implementation plan compatibility review' 
    assertions:
      - type: artifact_exists
        path: implementation/plan.json
      - type: artifact_exists
        path: implementation/index.md
      - type: artifact_exists
        path: implementation/compat-review.json

  - id: ts-029-implementation-plan-output-structure
    type: recipe_case
    inputs:
      title: Cross-cell sequencing
      description: Requires dependency jobs first
    mocks:
      ops:
        - match:
            node_path: new-ticket-implementation-planning/build_plan
          behavior:
            mode: return
            outputs:
              status: completed
              assistantSummary: '{"summary":"Cross-cell plan","requires_dependency_jobs":true,"dependency_order":["REQ-10","REQ-20"],"dependency_job_specs":[{"id":"REQ-10","title":"API prework","target_cell":"api","depends_on_ids":[],"depends_on_markdown":"- (none)","scope":"Prepare API","acceptance_criteria_markdown":"- API change available","risks_markdown":"- Coordination delay","notes":"Must land first"}],"local_steps":["Apply local integration"],"notes_for_user_review":"Dependency first"}'
            artifacts:
              implementation/plan.json: '{"summary":"Cross-cell plan","requires_dependency_jobs":true,"dependency_order":["REQ-10","REQ-20"],"dependency_job_specs":[{"id":"REQ-10","title":"API prework","target_cell":"api","depends_on_ids":[],"depends_on_markdown":"- (none)","scope":"Prepare API","acceptance_criteria_markdown":"- API change available","risks_markdown":"- Coordination delay","notes":"Must land first"}],"local_steps":["Apply local integration"],"notes_for_user_review":"Dependency first"}'
              implementation/index.md: '# Implementation plan\n- dependency first'
              implementation/REQ-10.md: '# REQ-10\nDependency job' 
        - match:
            node_path: new-ticket-implementation-planning/compat_review
          behavior:
            mode: return
            outputs:
              status: completed
              assistantSummary: '{"ok":true,"feedback":"Sequence valid","blocking_issues":[]}'
            artifacts:
              implementation/compat-review.json: '{"ok":true,"feedback":"Sequence valid","blocking_issues":[]}'
              implementation/compat-review.md: '# Implementation plan compatibility review' 
    assertions:
      - type: output_equals
        path: requires_dependency_jobs
        value: true
      - type: output_equals
        path: dependency_order
        value: [REQ-10, REQ-20]

  - id: ts-030-compat-review-blocking
    type: recipe_case
    inputs:
      title: Incompatible plan
      description: Compat review should block
    mocks:
      ops:
        - match:
            node_path: new-ticket-implementation-planning/build_plan
          behavior:
            mode: return
            outputs:
              status: completed
              assistantSummary: '{"summary":"Plan draft","requires_dependency_jobs":false,"dependency_order":["REQ-1"],"dependency_job_specs":[],"local_steps":["Draft changes"],"notes_for_user_review":"Review needed"}'
            artifacts:
              implementation/plan.json: '{"summary":"Plan draft","requires_dependency_jobs":false,"dependency_order":["REQ-1"],"dependency_job_specs":[],"local_steps":["Draft changes"],"notes_for_user_review":"Review needed"}'
              implementation/index.md: '# Implementation plan\n- draft'
              implementation/REQ-1.md: '# REQ-1\nDraft' 
        - match:
            node_path: new-ticket-implementation-planning/compat_review
          behavior:
            mode: return
            outputs:
              status: completed
              assistantSummary: '{"ok":false,"feedback":"Breaking interface detected","blocking_issues":["Backwards compatibility violation"]}'
            artifacts:
              implementation/compat-review.json: '{"ok":false,"feedback":"Breaking interface detected","blocking_issues":["Backwards compatibility violation"]}'
              implementation/compat-review.md: '# Implementation plan compatibility review' 
    assertions:
      - type: output_equals
        path: compat_review_ok
        value: false
      - type: output_equals
        path: compat_review_blocking_issues
        value: [Backwards compatibility violation]
```
