# ticket-merge test suite

```yaml
cases:
  - id: ts-019-prompts-when-details-missing
    type: recipe_case
    inputs:
      local_hash: 0123456789abcdef0123456789abcdef01234567
    mocks:
      ops:
        - match:
            node_path: ticket-merge/prompt/input
          behavior:
            mode: return
            outputs:
              fields:
                upstream_repo: git@example.com:org/repo.git
                upstream_branch: main
                commit_message: Merge feature
        - match:
            node_path: ticket-merge/approve/input
          behavior:
            mode: return
            outputs:
              response: cancel
    assertions:
      - type: node_executed
        node_path: ticket-merge/prompt/input
      - type: output_equals
        path: merged
        value: false

  - id: ts-020-cancel-skips-merge
    type: recipe_case
    inputs:
      local_hash: 0123456789abcdef0123456789abcdef01234567
      upstream_repo: git@example.com:org/repo.git
      upstream_branch: main
      commit_message: Merge feature
    mocks:
      ops:
        - match:
            node_path: ticket-merge/approve/input
          behavior:
            mode: return
            outputs:
              response: cancel
    assertions:
      - type: output_equals
        path: merged
        value: false
      - type: output_equals
        path: merged_hash
        value: ""

  - id: ts-021-approve-merge-returns-hash
    type: recipe_case
    inputs:
      local_hash: 0123456789abcdef0123456789abcdef01234567
      upstream_repo: git@example.com:org/repo.git
      upstream_branch: main
      commit_message: Merge feature
    mocks:
      ops:
        - match:
            node_path: ticket-merge/approve/input
          behavior:
            mode: return
            outputs:
              response: merge
        - match:
            node_path: ticket-merge/merge/squashrebasemerge
          behavior:
            mode: return
            outputs:
              merged_hash: deadbeef
              target_branch: main
    assertions:
      - type: output_equals
        path: merged
        value: true
      - type: output_equals
        path: merged_hash
        value: deadbeef

  - id: ts-022-prompted-merge-path
    type: recipe_case
    inputs:
      local_hash: 0123456789abcdef0123456789abcdef01234567
    mocks:
      ops:
        - match:
            node_path: ticket-merge/prompt/input
          behavior:
            mode: return
            outputs:
              fields:
                upstream_repo: git@example.com:org/repo.git
                upstream_branch: release
                commit_message: Prompted merge
        - match:
            node_path: ticket-merge/approve/input
          behavior:
            mode: return
            outputs:
              response: merge
        - match:
            node_path: ticket-merge/merge/squashrebasemerge
          behavior:
            mode: return
            outputs:
              merged_hash: cafe1234
              target_branch: release
    assertions:
      - type: node_executed
        node_path: ticket-merge/merge/squashrebasemerge
      - type: output_equals
        path: target_branch
        value: release

  - id: ts-023-target-branch-output
    type: recipe_case
    inputs:
      local_hash: 0123456789abcdef0123456789abcdef01234567
      upstream_repo: git@example.com:org/repo.git
      upstream_branch: develop
      commit_message: Merge feature
    mocks:
      ops:
        - match:
            node_path: ticket-merge/approve/input
          behavior:
            mode: return
            outputs:
              response: merge
        - match:
            node_path: ticket-merge/merge/squashrebasemerge
          behavior:
            mode: return
            outputs:
              merged_hash: b00b135
              target_branch: develop
    assertions:
      - type: output_equals
        path: target_branch
        value: develop
      - type: output_equals
        path: merged
        value: true

  - id: guard-skip-merge-without-local-hash
    type: recipe_case
    inputs:
      upstream_repo: git@example.com:org/repo.git
      upstream_branch: main
      commit_message: Merge feature
    mocks:
      ops:
        - match:
            node_path: ticket-merge/approve/input
          behavior:
            mode: return
            outputs:
              response: merge
        - match:
            node_path: ticket-merge/skipped_no_changes/sleep
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
```
