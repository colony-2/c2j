# skill-quality-smoke test suite

```yaml
cases:
  - id: ts-042-live-skill-quality-smoke
    type: recipe_case
    assertions:
      - type: output_equals
        path: triage_local_ok
        value: true
      - type: output_equals
        path: triage_local_rationale_nonempty
        value: true
      - type: output_equals
        path: triage_frontend_redirect
        value: true
      - type: output_equals
        path: triage_frontend_target
        value: frontend
      - type: output_equals
        path: requirements_review_good_ok
        value: true
      - type: output_equals
        path: requirements_count_nonzero
        value: true
      - type: output_equals
        path: requirements_cross_cell_false
        value: true
      - type: output_equals
        path: requirements_targets_valid
        value: true
      - type: output_equals
        path: requirements_acceptance_nonempty
        value: true
      - type: output_equals
        path: requirements_open_questions_reasonable
        value: true
      - type: output_equals
        path: requirements_bad_review_rejects
        value: true
      - type: output_equals
        path: requirements_bad_blockers_nonempty
        value: true
      - type: output_equals
        path: implementation_review_good_ok
        value: true
      - type: output_equals
        path: implementation_requires_dependency_jobs_false
        value: true
      - type: output_equals
        path: implementation_local_steps_nonzero
        value: true
      - type: output_equals
        path: implementation_has_validation_step
        value: true
      - type: output_equals
        path: implementation_bad_review_rejects
        value: true
      - type: output_equals
        path: implementation_bad_blockers_nonempty
        value: true
      - type: output_equals
        path: outcome_review_good_ok
        value: true
      - type: output_equals
        path: outcome_validation_commands_nonempty
        value: true
      - type: output_equals
        path: outcome_repo_glob
        value: .c2/tests/*.md
      - type: output_equals
        path: outcome_statement_files_exist
        value: true
      - type: output_equals
        path: outcome_statement_annotations_present
        value: true
      - type: output_equals
        path: outcome_positive_and_negative_present
        value: true
      - type: output_equals
        path: outcome_bad_review_rejects
        value: true
      - type: output_equals
        path: outcome_bad_blockers_nonempty
        value: true
```
