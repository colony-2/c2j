# new-ticket test suite

```yaml
cases:
  - id: ts-031-dependency-tickets-gate-local-implementation
    type: recipe_case
    inputs:
      title: Cross-cell change
      description: Requires dependency tickets first
    mocks:
      ops:
        - match:
            node_path: new-ticket/triage/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              outputs:
                cell_is_appropriate: true
                recommended_cell: recipe-tests
                recommended_cell_is_valid: true
                recommended_cell_final: recipe-tests
                rationale: In-cell
        - match:
            node_path: new-ticket/requirements_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              api_review_ok: true
              outputs:
                plan_json: '{"summary":"Reqs"}'
                summary: Requirements ready
                needs_cross_cell_support: true
                dependency_order: [REQ-1, REQ-2]
                requirements:
                  - id: REQ-1
                    title: API prep
                    target_cell: api
                    depends_on: []
                    scope: Prepare API contract
                    api_changes: []
                    acceptance_criteria: [API contract ready]
                    risks: [Coordination]
                    open_questions: []
                  - id: REQ-2
                    title: Local integration
                    target_cell: recipe-tests
                    depends_on: [REQ-1]
                    scope: Wire local usage
                    api_changes: []
                    acceptance_criteria: [Local integration works]
                    risks: []
                    open_questions: []
                notes_for_user_review: Review requirements
                api_review_ok: true
                api_review_feedback: Compatible
                api_review_blocking_issues: []
            artifacts:
              requirements/plan.json: '{"summary":"Reqs"}'
              requirements/index.md: '# Requirements'
              requirements/api-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/implementation_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              compat_review_ok: true
              requires_dependency_tickets: true
              outputs:
                plan_json: '{"summary":"Dependency tickets required","requires_dependency_tickets":true,"dependency_order":["REQ-1","REQ-2"],"dependency_ticket_specs":[{"id":"REQ-1","title":"API prep","target_cell":"api","depends_on_ids":[],"depends_on_markdown":"- (none)","scope":"Prepare API contract","acceptance_criteria_markdown":"- API contract ready","risks_markdown":"- Coordination delay","notes":"Must complete before local work"}],"local_steps":["Implement REQ-2 after REQ-1"],"notes_for_user_review":"Wait on dependency","compat_review_ok":true,"compat_review_feedback":"Compatible","compat_review_blocking_issues":[]}'
                summary: Dependency tickets required
                requires_dependency_tickets: true
                dependency_order: [REQ-1, REQ-2]
                dependency_ticket_specs:
                  - id: REQ-1
                    title: API prep
                    target_cell: api
                    depends_on_ids: []
                    depends_on_markdown: "- (none)"
                    scope: Prepare API contract
                    acceptance_criteria_markdown: "- API contract ready"
                    risks_markdown: "- Coordination delay"
                    notes: Must complete before local work
                local_steps: [Implement REQ-2 after REQ-1]
                notes_for_user_review: Wait on dependency
                compat_review_ok: true
                compat_review_feedback: Compatible
                compat_review_blocking_issues: []
            artifacts:
              implementation/plan.json: '{"summary":"Impl"}'
              implementation/index.md: '# Implementation'
              implementation/compat-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/spawn_dependency_tickets/ticket.manage
          behavior:
            mode: return
            outputs:
              results:
                - ticket:
                    id: T-dep-1
        - match:
            node_path: new-ticket/dependency_wait_hold/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
    assertions:
      - type: node_executed
        node_path: new-ticket/spawn_dependency_tickets/ticket.manage
      - type: output_equals
        path: dependency_waiting
        value: true

  - id: ts-032-cancel-from-merge-review
    type: recipe_case
    inputs:
      title: Local implementation
      description: Cancel path
    mocks:
      ops:
        - match:
            node_path: new-ticket/triage/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              outputs:
                cell_is_appropriate: true
                recommended_cell: recipe-tests
                recommended_cell_is_valid: true
                recommended_cell_final: recipe-tests
                rationale: In-cell
        - match:
            node_path: new-ticket/requirements_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              api_review_ok: true
              outputs:
                plan_json: '{"summary":"Reqs"}'
                summary: Requirements ready
                needs_cross_cell_support: false
                dependency_order: [REQ-1]
                requirements:
                  - id: REQ-1
                    title: Local change
                    target_cell: recipe-tests
                    depends_on: []
                    scope: Implement locally
                    api_changes: []
                    acceptance_criteria: [Local behavior updated]
                    risks: []
                    open_questions: []
                notes_for_user_review: Review requirements
                api_review_ok: true
                api_review_feedback: Compatible
                api_review_blocking_issues: []
            artifacts:
              requirements/plan.json: '{"summary":"Reqs"}'
              requirements/index.md: '# Requirements'
              requirements/api-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/implementation_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              compat_review_ok: true
              outputs:
                plan_json: '{"summary":"Impl"}'
                summary: Local plan
                requires_dependency_tickets: false
                dependency_order: [REQ-1]
                dependency_ticket_specs: []
                local_steps: [Implement feature]
                notes_for_user_review: Ready
                compat_review_ok: true
                compat_review_feedback: Compatible
                compat_review_blocking_issues: []
            artifacts:
              implementation/plan.json: '{"summary":"Impl"}'
              implementation/index.md: '# Implementation'
              implementation/compat-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/outcome_determination/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              review_ok: true
              outputs:
                plan_json: '{"summary":"Outcome"}'
                summary: Outcome
                current_test_statements_summary: Existing statements cover baseline
                test_statement_updates_required: true
                test_statement_repo_glob: ".c2/tests/*.md"
                validation_commands: "npm test"
                notes_for_user_review: Add negative path checks
                review_ok: true
                review_feedback: Looks good
                review_blocking_issues: []
            artifacts:
              outcome/plan.json: '{"summary":"Outcome"}'
              outcome/index.md: '# Outcome'
              outcome/tests-index.md: '# Tests index'
              outcome/validation-commands.txt: npm test
              outcome/review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/pre_implementation_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: continue
                feedback: ""
                implementation_answers: ""
        - match:
            node_path: new-ticket/implement/codex.exec
          behavior:
            mode: return
            outputs:
              status: completed
              sessionId: sid-1
              assistantSummary: Implemented changes
              incompleteReason: ""
              incompleteCategory: ""
              pendingDependencies: []
        - match:
            node_path: new-ticket/validate/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              passed: true
              outputs:
                passed: true
            artifacts:
              validation/output.txt: ok
              validation/output-tail.txt: ok-tail
        - match:
            node_path: new-ticket/ready_to_merge_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: cancel_ticket
                feedback: ""
                upstream_repo: ""
                upstream_branch: ""
                commit_message: ""
        - match:
            node_path: new-ticket/cancel_ticket_route/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
        - match:
            node_path: new-ticket/cancel_ticket_done/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
    assertions:
      - type: node_executed
        node_path: new-ticket/cancel_ticket_route/sleep
      - type: output_equals
        path: canceled
        value: true

  - id: ts-033-merge-without-hash-forces-resolution
    type: recipe_case
    inputs:
      title: Merge without hash
      description: Merge decision should return to implementation when no hash exists
    mocks:
      ops:
        - match:
            node_path: new-ticket/triage/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              outputs:
                cell_is_appropriate: true
                recommended_cell: recipe-tests
                recommended_cell_is_valid: true
                recommended_cell_final: recipe-tests
                rationale: In-cell
        - match:
            node_path: new-ticket/requirements_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              api_review_ok: true
              outputs:
                plan_json: '{"summary":"Reqs"}'
                summary: Requirements ready
                needs_cross_cell_support: false
                dependency_order: [REQ-1]
                requirements:
                  - id: REQ-1
                    title: Local change
                    target_cell: recipe-tests
                    depends_on: []
                    scope: Implement locally
                    api_changes: []
                    acceptance_criteria: [Local behavior updated]
                    risks: []
                    open_questions: []
                notes_for_user_review: Review requirements
                api_review_ok: true
                api_review_feedback: Compatible
                api_review_blocking_issues: []
            artifacts:
              requirements/plan.json: '{"summary":"Reqs"}'
              requirements/index.md: '# Requirements'
              requirements/api-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/implementation_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              compat_review_ok: true
              outputs:
                plan_json: '{"summary":"Impl"}'
                summary: Local plan
                requires_dependency_tickets: false
                dependency_order: [REQ-1]
                dependency_ticket_specs: []
                local_steps: [Implement feature]
                notes_for_user_review: Ready
                compat_review_ok: true
                compat_review_feedback: Compatible
                compat_review_blocking_issues: []
            artifacts:
              implementation/plan.json: '{"summary":"Impl"}'
              implementation/index.md: '# Implementation'
              implementation/compat-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/outcome_determination/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              review_ok: true
              outputs:
                plan_json: '{"summary":"Outcome"}'
                summary: Outcome
                current_test_statements_summary: Existing statements cover baseline
                test_statement_updates_required: true
                test_statement_repo_glob: ".c2/tests/*.md"
                validation_commands: "npm test"
                notes_for_user_review: Add negative path checks
                review_ok: true
                review_feedback: Looks good
                review_blocking_issues: []
            artifacts:
              outcome/plan.json: '{"summary":"Outcome"}'
              outcome/index.md: '# Outcome'
              outcome/tests-index.md: '# Tests index'
              outcome/validation-commands.txt: npm test
              outcome/review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/pre_implementation_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: continue
                feedback: ""
                implementation_answers: "Proceed to merge check."
        - match:
            node_path: new-ticket/implement/codex.exec
          behavior:
            mode: return
            outputs:
              status: completed
              sessionId: sid-h0
              assistantSummary: Pre-merge implementation completed
              incompleteReason: ""
              incompleteCategory: ""
              pendingDependencies: []
        - match:
            node_path: new-ticket/validate/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              passed: true
              outputs:
                passed: true
            artifacts:
              validation/output.txt: ok
              validation/output-tail.txt: ok-tail
        - match:
            node_path: new-ticket/ready_to_merge_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: merge
                feedback: ""
                upstream_repo: ""
                upstream_branch: ""
                commit_message: ""
        - match:
            node_path: new-ticket/implement_resume/codex.exec
          behavior:
            mode: return
            outputs:
              status: incomplete
              sessionId: sid-h1
              assistantSummary: Need clarification before merge
              incompleteReason: "1. Confirm expected merge behavior with no local hash."
              incompleteCategory: needs_user_input
              pendingDependencies: []
        - match:
            node_path: new-ticket/pre_implementation_followup_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: cancel_ticket
                feedback: ""
                implementation_answers: ""
        - match:
            node_path: new-ticket/cancel_ticket_route/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
        - match:
            node_path: new-ticket/cancel_ticket_done/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
    assertions:
      - type: output_equals
        path: merged
        value: false
      - type: output_equals
        path: canceled
        value: true

  - id: ts-034-merge-success-completes-ticket
    type: recipe_case
    inputs:
      title: Merge success
      description: Complete ticket after merge
      local_hash: deadbeefdeadbeefdeadbeefdeadbeefdeadbeef
      upstream_repo: git@example.com:org/repo.git
      upstream_branch: main
    mocks:
      ops:
        - match:
            node_path: new-ticket/triage/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              outputs:
                cell_is_appropriate: true
                recommended_cell: recipe-tests
                recommended_cell_is_valid: true
                recommended_cell_final: recipe-tests
                rationale: In-cell
        - match:
            node_path: new-ticket/requirements_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              api_review_ok: true
              outputs:
                plan_json: '{"summary":"Reqs"}'
                summary: Requirements ready
                needs_cross_cell_support: false
                dependency_order: [REQ-1]
                requirements:
                  - id: REQ-1
                    title: Local change
                    target_cell: recipe-tests
                    depends_on: []
                    scope: Implement locally
                    api_changes: []
                    acceptance_criteria: [Local behavior updated]
                    risks: []
                    open_questions: []
                notes_for_user_review: Review requirements
                api_review_ok: true
                api_review_feedback: Compatible
                api_review_blocking_issues: []
            artifacts:
              requirements/plan.json: '{"summary":"Reqs"}'
              requirements/index.md: '# Requirements'
              requirements/api-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/implementation_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              compat_review_ok: true
              outputs:
                plan_json: '{"summary":"Impl"}'
                summary: Local plan
                requires_dependency_tickets: false
                dependency_order: [REQ-1]
                dependency_ticket_specs: []
                local_steps: [Implement feature]
                notes_for_user_review: Ready
                compat_review_ok: true
                compat_review_feedback: Compatible
                compat_review_blocking_issues: []
            artifacts:
              implementation/plan.json: '{"summary":"Impl"}'
              implementation/index.md: '# Implementation'
              implementation/compat-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/outcome_determination/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              review_ok: true
              outputs:
                plan_json: '{"summary":"Outcome"}'
                summary: Outcome
                current_test_statements_summary: Existing statements cover baseline
                test_statement_updates_required: true
                test_statement_repo_glob: ".c2/tests/*.md"
                validation_commands: "npm test"
                notes_for_user_review: Add negative path checks
                review_ok: true
                review_feedback: Looks good
                review_blocking_issues: []
            artifacts:
              outcome/plan.json: '{"summary":"Outcome"}'
              outcome/index.md: '# Outcome'
              outcome/tests-index.md: '# Tests index'
              outcome/validation-commands.txt: npm test
              outcome/review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/pre_implementation_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: continue
                feedback: ""
                implementation_answers: ""
        - match:
            node_path: new-ticket/implement/codex.exec
          behavior:
            mode: return
            outputs:
              status: completed
              sessionId: sid-merge
              assistantSummary: Implemented changes
              incompleteReason: ""
              incompleteCategory: ""
              pendingDependencies: []
        - match:
            node_path: new-ticket/validate/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              passed: true
              outputs:
                passed: true
            artifacts:
              validation/output.txt: ok
              validation/output-tail.txt: ok-tail
        - match:
            node_path: new-ticket/ready_to_merge_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: merge
                feedback: ""
                upstream_repo: ""
                upstream_branch: ""
                commit_message: ""
        - match:
            node_path: new-ticket/merge/squashrebasemerge
          behavior:
            mode: return
            outputs:
              merged_hash: cafebabecafebabecafebabecafebabecafebabe
              target_branch: main
        - match:
            node_path: new-ticket/complete_done_route/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
        - match:
            node_path: new-ticket/complete_done_noop/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
    assertions:
      - type: output_equals
        path: merged
        value: true
      - type: output_equals
        path: merged_hash
        value: cafebabecafebabecafebabecafebabecafebabe
      - type: output_equals
        path: ticket_done
        value: true

  - id: ts-035-implementation-opens-cross-cell-bug-tickets
    type: recipe_case
    inputs:
      title: Implementation finds external bug
      description: Bug ticket should be created
    mocks:
      ops:
        - match:
            node_path: new-ticket/triage/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              outputs:
                cell_is_appropriate: true
                recommended_cell: recipe-tests
                recommended_cell_is_valid: true
                recommended_cell_final: recipe-tests
                rationale: In-cell
        - match:
            node_path: new-ticket/requirements_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              api_review_ok: true
              outputs:
                plan_json: '{"summary":"Reqs"}'
                summary: Requirements ready
                needs_cross_cell_support: false
                dependency_order: [REQ-1]
                requirements:
                  - id: REQ-1
                    title: Local change
                    target_cell: recipe-tests
                    depends_on: []
                    scope: Implement locally
                    api_changes: []
                    acceptance_criteria: [Local behavior updated]
                    risks: []
                    open_questions: []
                notes_for_user_review: Review requirements
                api_review_ok: true
                api_review_feedback: Compatible
                api_review_blocking_issues: []
            artifacts:
              requirements/plan.json: '{"summary":"Reqs"}'
              requirements/index.md: '# Requirements'
              requirements/api-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/implementation_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              compat_review_ok: true
              outputs:
                plan_json: '{"summary":"Impl"}'
                summary: Local plan
                requires_dependency_tickets: false
                dependency_order: [REQ-1]
                dependency_ticket_specs: []
                local_steps: [Implement feature]
                notes_for_user_review: Ready
                compat_review_ok: true
                compat_review_feedback: Compatible
                compat_review_blocking_issues: []
            artifacts:
              implementation/plan.json: '{"summary":"Impl"}'
              implementation/index.md: '# Implementation'
              implementation/compat-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/outcome_determination/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              review_ok: true
              outputs:
                plan_json: '{"summary":"Outcome"}'
                summary: Outcome
                current_test_statements_summary: Existing statements cover baseline
                test_statement_updates_required: true
                test_statement_repo_glob: ".c2/tests/*.md"
                validation_commands: "npm test"
                notes_for_user_review: Add negative path checks
                review_ok: true
                review_feedback: Looks good
                review_blocking_issues: []
            artifacts:
              outcome/plan.json: '{"summary":"Outcome"}'
              outcome/index.md: '# Outcome'
              outcome/tests-index.md: '# Tests index'
              outcome/validation-commands.txt: npm test
              outcome/review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/pre_implementation_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: continue
                feedback: ""
                implementation_answers: ""
        - match:
            node_path: new-ticket/implement/codex.exec
          behavior:
            mode: return
            outputs:
              status: completed
              sessionId: sid-bug
              assistantSummary: Found bug in api cell
              incompleteReason: ""
              incompleteCategory: ""
              pendingDependencies:
                - component: api
                  requestedChanges: "Bug: null pointer in API validation path.\nRepro: send empty payload to /v1/items."
        - match:
            node_path: new-ticket/implement_bugs/ticket.manage
          behavior:
            mode: return
            outputs:
              results:
                - ticket:
                    id: T-bug-1
        - match:
            node_path: new-ticket/validate/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              passed: true
              outputs:
                passed: true
            artifacts:
              validation/output.txt: ok
              validation/output-tail.txt: ok-tail
        - match:
            node_path: new-ticket/ready_to_merge_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: cancel_ticket
                feedback: ""
                upstream_repo: ""
                upstream_branch: ""
                commit_message: ""
        - match:
            node_path: new-ticket/cancel_ticket_route/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
        - match:
            node_path: new-ticket/cancel_ticket_done/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
    assertions:
      - type: node_executed
        node_path: new-ticket/implement_bugs/ticket.manage
      - type: output_equals
        path: canceled
        value: true

  - id: ts-036-implementation-questions-pause-and-resume
    type: recipe_case
    inputs:
      title: Implementation needs clarification
      description: Prompt user and resume codex
    mocks:
      ops:
        - match:
            node_path: new-ticket/triage/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              outputs:
                cell_is_appropriate: true
                recommended_cell: recipe-tests
                recommended_cell_is_valid: true
                recommended_cell_final: recipe-tests
                rationale: In-cell
        - match:
            node_path: new-ticket/requirements_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              api_review_ok: true
              outputs:
                plan_json: '{"summary":"Reqs"}'
                summary: Requirements ready
                needs_cross_cell_support: false
                dependency_order: [REQ-1]
                requirements:
                  - id: REQ-1
                    title: Local change
                    target_cell: recipe-tests
                    depends_on: []
                    scope: Implement locally
                    api_changes: []
                    acceptance_criteria: [Local behavior updated]
                    risks: []
                    open_questions: []
                notes_for_user_review: Review requirements
                api_review_ok: true
                api_review_feedback: Compatible
                api_review_blocking_issues: []
            artifacts:
              requirements/plan.json: '{"summary":"Reqs"}'
              requirements/index.md: '# Requirements'
              requirements/api-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/implementation_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              compat_review_ok: true
              outputs:
                plan_json: '{"summary":"Impl"}'
                summary: Local plan
                requires_dependency_tickets: false
                dependency_order: [REQ-1]
                dependency_ticket_specs: []
                local_steps: [Implement feature]
                notes_for_user_review: Ready
                compat_review_ok: true
                compat_review_feedback: Compatible
                compat_review_blocking_issues: []
            artifacts:
              implementation/plan.json: '{"summary":"Impl"}'
              implementation/index.md: '# Implementation'
              implementation/compat-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/outcome_determination/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              review_ok: true
              outputs:
                plan_json: '{"summary":"Outcome"}'
                summary: Outcome
                current_test_statements_summary: Existing statements cover baseline
                test_statement_updates_required: true
                test_statement_repo_glob: ".c2/tests/*.md"
                validation_commands: "npm test"
                notes_for_user_review: Add negative path checks
                review_ok: true
                review_feedback: Looks good
                review_blocking_issues: []
            artifacts:
              outcome/plan.json: '{"summary":"Outcome"}'
              outcome/index.md: '# Outcome'
              outcome/tests-index.md: '# Tests index'
              outcome/validation-commands.txt: npm test
              outcome/review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/pre_implementation_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: continue
                feedback: ""
                implementation_answers: ""
        - match:
            node_path: new-ticket/implement/codex.exec
          behavior:
            mode: return
            outputs:
              status: incomplete
              sessionId: sid-q1
              assistantSummary: Needs clarification
              incompleteReason: "1. Should retries be enabled globally or endpoint-specific?"
              incompleteCategory: needs_user_input
              pendingDependencies: []
        - match:
            node_path: new-ticket/pre_implementation_followup_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: continue
                feedback: ""
                implementation_answers: "1) Endpoint-specific retries only."
        - match:
            node_path: new-ticket/implement_resume/codex.exec
          behavior:
            mode: return
            outputs:
              status: completed
              sessionId: sid-q1
              assistantSummary: Clarification applied and implementation completed
              incompleteReason: ""
              incompleteCategory: ""
              pendingDependencies: []
        - match:
            node_path: new-ticket/validate/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              passed: true
              outputs:
                passed: true
            artifacts:
              validation/output.txt: ok
              validation/output-tail.txt: ok-tail
        - match:
            node_path: new-ticket/ready_to_merge_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: cancel_ticket
                feedback: ""
                upstream_repo: ""
                upstream_branch: ""
                commit_message: ""
        - match:
            node_path: new-ticket/cancel_ticket_route/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
        - match:
            node_path: new-ticket/cancel_ticket_done/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
    assertions:
      - type: node_executed
        node_path: new-ticket/pre_implementation_review/input
      - type: node_executed
        node_path: new-ticket/implement/codex.exec
      - type: output_equals
        path: implementation_user_questions_requested
        value: true

  - id: ts-040-outcome-commands-feed-validation-default
    type: recipe_case
    inputs:
      title: Use outcome validation commands
      description: Validation should default to outcome command plan
    mocks:
      ops:
        - match:
            node_path: new-ticket/triage/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              outputs:
                cell_is_appropriate: true
                recommended_cell: recipe-tests
                recommended_cell_is_valid: true
                recommended_cell_final: recipe-tests
                rationale: In-cell
        - match:
            node_path: new-ticket/requirements_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              api_review_ok: true
              outputs:
                plan_json: '{"summary":"Reqs"}'
                summary: Requirements ready
                needs_cross_cell_support: false
                dependency_order: [REQ-1]
                requirements:
                  - id: REQ-1
                    title: Local change
                    target_cell: recipe-tests
                    depends_on: []
                    scope: Implement locally
                    api_changes: []
                    acceptance_criteria: [Local behavior updated]
                    risks: []
                    open_questions: []
                notes_for_user_review: Review requirements
                api_review_ok: true
                api_review_feedback: Compatible
                api_review_blocking_issues: []
            artifacts:
              requirements/plan.json: '{"summary":"Reqs"}'
              requirements/index.md: '# Requirements'
              requirements/api-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/implementation_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              compat_review_ok: true
              outputs:
                plan_json: '{"summary":"Impl"}'
                summary: Local plan
                requires_dependency_tickets: false
                dependency_order: [REQ-1]
                dependency_ticket_specs: []
                local_steps: [Implement feature]
                notes_for_user_review: Ready
                compat_review_ok: true
                compat_review_feedback: Compatible
                compat_review_blocking_issues: []
            artifacts:
              implementation/plan.json: '{"summary":"Impl"}'
              implementation/index.md: '# Implementation'
              implementation/compat-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/outcome_determination/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              review_ok: true
              outputs:
                plan_json: '{"summary":"Outcome"}'
                summary: Outcome
                current_test_statements_summary: Existing statements cover baseline
                test_statement_updates_required: true
                test_statement_repo_glob: ".c2/tests/*.md"
                validation_commands: "npm test"
                notes_for_user_review: Add negative path checks
                review_ok: true
                review_feedback: Looks good
                review_blocking_issues: []
            artifacts:
              outcome/plan.json: '{"summary":"Outcome"}'
              outcome/index.md: '# Outcome'
              outcome/tests-index.md: '# Tests index'
              outcome/validation-commands.txt: npm test
              outcome/review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/pre_implementation_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: continue
                feedback: ""
                implementation_answers: ""
        - match:
            node_path: new-ticket/implement/codex.exec
          behavior:
            mode: return
            outputs:
              status: completed
              sessionId: sid-validation
              assistantSummary: Implemented changes
              incompleteReason: ""
              incompleteCategory: ""
              pendingDependencies: []
        - match:
            node_path: new-ticket/validate/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              passed: true
              outputs:
                passed: true
            artifacts:
              validation/output.txt: ok
              validation/output-tail.txt: ok-tail
        - match:
            node_path: new-ticket/ready_to_merge_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: cancel_ticket
                feedback: ""
                upstream_repo: ""
                upstream_branch: ""
                commit_message: ""
        - match:
            node_path: new-ticket/cancel_ticket_route/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
        - match:
            node_path: new-ticket/cancel_ticket_done/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
    assertions:
      - type: node_executed
        node_path: new-ticket/pre_implementation_review/input
      - type: output_equals
        path: validation_selected_commands
        value: npm test

  - id: ts-041-implementation-requests-test-statement-update
    type: recipe_case
    inputs:
      title: Implementation requests statement revision
      description: Request should route to follow-up review gate
    mocks:
      ops:
        - match:
            node_path: new-ticket/triage/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              outputs:
                cell_is_appropriate: true
                recommended_cell: recipe-tests
                recommended_cell_is_valid: true
                recommended_cell_final: recipe-tests
                rationale: In-cell
        - match:
            node_path: new-ticket/requirements_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              api_review_ok: true
              outputs:
                plan_json: '{"summary":"Reqs"}'
                summary: Requirements ready
                needs_cross_cell_support: false
                dependency_order: [REQ-1]
                requirements:
                  - id: REQ-1
                    title: Local change
                    target_cell: recipe-tests
                    depends_on: []
                    scope: Implement locally
                    api_changes: []
                    acceptance_criteria: [Local behavior updated]
                    risks: []
                    open_questions: []
                notes_for_user_review: Review requirements
                api_review_ok: true
                api_review_feedback: Compatible
                api_review_blocking_issues: []
            artifacts:
              requirements/plan.json: '{"summary":"Reqs"}'
              requirements/index.md: '# Requirements'
              requirements/api-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/implementation_planning/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              compat_review_ok: true
              outputs:
                plan_json: '{"summary":"Impl"}'
                summary: Local plan
                requires_dependency_tickets: false
                dependency_order: [REQ-1]
                dependency_ticket_specs: []
                local_steps: [Implement feature]
                notes_for_user_review: Ready
                compat_review_ok: true
                compat_review_feedback: Compatible
                compat_review_blocking_issues: []
            artifacts:
              implementation/plan.json: '{"summary":"Impl"}'
              implementation/index.md: '# Implementation'
              implementation/compat-review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/outcome_determination/recipe.run_and_get_result
          behavior:
            mode: return
            outputs:
              review_ok: true
              outputs:
                plan_json: '{"summary":"Outcome"}'
                summary: Outcome
                current_test_statements_summary: Existing statements cover baseline
                test_statement_updates_required: true
                test_statement_repo_glob: ".c2/tests/*.md"
                validation_commands: "npm test"
                notes_for_user_review: Add negative path checks
                review_ok: true
                review_feedback: Looks good
                review_blocking_issues: []
            artifacts:
              outcome/plan.json: '{"summary":"Outcome"}'
              outcome/index.md: '# Outcome'
              outcome/tests-index.md: '# Tests index'
              outcome/validation-commands.txt: npm test
              outcome/review.json: '{"ok":true}'
        - match:
            node_path: new-ticket/pre_implementation_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: continue
                feedback: ""
                implementation_answers: ""
        - match:
            node_path: new-ticket/implement/codex.exec
          behavior:
            mode: return
            outputs:
              status: incomplete
              sessionId: sid-ts-update
              assistantSummary: Needs test statement update
              incompleteReason: "Add negative case for retries exhausted path in .c2/tests/service.md."
              incompleteCategory: needs_test_statement_update
              pendingDependencies: []
        - match:
            node_path: new-ticket/pre_implementation_followup_review/input
          behavior:
            mode: return
            outputs:
              fields:
                decision: cancel_ticket
                feedback: ""
                implementation_answers: ""
        - match:
            node_path: new-ticket/cancel_ticket_route/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
        - match:
            node_path: new-ticket/cancel_ticket_done/sleep
          behavior:
            mode: return
            outputs:
              completed: true
              interrupted: false
              actual_duration: 1ms
              error_message: ""
    assertions:
      - type: node_executed
        node_path: new-ticket/pre_implementation_review/input
      - type: output_equals
        path: implementation_requested_test_statement_update
        value: true
      - type: output_equals
        path: canceled
        value: true
```
