<!-- Sources: README.md; pkg/worker/export/exports.go; pkg/worker/commandop/command_execution.go; pkg/ops/sleepop/op.go; pkg/ops/extensions/execution_op.go; pkg/ops/recipe/op.go; pkg/ops/recipe/await_result_soft.go; pkg/git/export/exports.go; pkg/git/*/wrapper.go; pkg/worker/test-fixtures/test_ops.go. -->

# Ops Catalog

## Core Worker Ops

### `command_execution`

Run a shell command directly in the c2j worker environment.

Inputs:

- `run` required.
- `working_directory`, defaulting to the worktree path.
- `shell`: `bash`, `sh`, `powershell`, or `cmd`.
- `env`: map of strings.
- `continue_on_error`: keep output instead of failing on non-zero exit.
- `timeout`: Go duration string.

Outputs: `stdout`, `stderr`, `exit_code`, `success`, `timed_out`, `error_message`.

```yaml
op: command_execution
inputs:
  run: "go test ./..."
  timeout: 2m
```

### `sleep`

Pause execution.

```yaml
op: sleep
inputs:
  duration: 30s
```

Outputs include `start_time`, `end_time`, `actual_duration`, `completed`, `interrupted`, and `error_message`.

### `extension_execution`

Run a selector-backed extension op.

```yaml
op: extension_execution
inputs:
  selector: git+https://github.com/colony-2/c2ops.git//codex@main
  inputs:
    prompt: "{{ inputs.prompt }}"
```

Inputs: `selector`, nested `inputs`, optional `repository_source`, and optional `repository_ref`. Selector manifests must include `input_schema` and `output_schema`.

## Recipe Child Ops

- `recipe.run_and_get_result`: start one child recipe, wait, return outputs.
- `recipes.run`: start many child recipes and return job IDs.
- `recipes.run_and_wait`: start many child recipes and wait.
- `recipe.await_result`: wait for a child job and return recipe output.
- `recipe.await_result_soft`: inspect a child terminal state without failing hard on child failure/cancel.
- `recipe.get_result`: fetch a completed recipe result.

Single recipe inputs include `name`, `cell_name`, `inputs`, `artifacts`, and `git`. `SingleRecipeWithRef` also uses `git_ref`, defaulting to the current git hash when present, otherwise current ref.

## Git Ops

### `git_file_collector`

Collect repository files for review or LLM input.

Inputs include `context_dir`, `file_patterns`, `exclude_patterns`, `max_file_size`, `max_total_size`, `include_staged`, `include_untracked`, `use_gitignore`, `exclude_binary`, `auto_detect_type`, and `include_metadata`.

### `git_shallow_clone`

Clone a local git repository at a specific commit.

Inputs: `source_dir`, `target_dir`, `commit_hash`.

### `git_persist_commit`

Capture a commit and write a portable thin pack.

Inputs: `repo_path`, `storage_location`, `root_hash`, `commit_message`, `author`, `timeout`.

### `git_restore_commit`

Restore a commit from local history or thin packs.

Inputs: `repo_path`, `target_commit`, `root_hash`, `storage_location`, `force`, `timeout`.

### `thinpackrebase`

Rebase a thin-pack backed workspace onto a new base commit and refresh git context metadata.

Inputs: `repo_path`, `target_base_hash`, `upstream_remote`, `preserve_author`, `update_refs`, `base_hash`, `persist_hash`, `base_repo`, `git_author`, and `cell_name`.

### `squashrebasemerge`

Squash local commits, rebase onto the target branch tip, and fast-forward merge back to the remote.

Inputs: `repo_path`, `local_hash`, `upstream_repo`, `upstream_branch`, `rebase`, `author`, and `commit_message`.

## Test Fixture Ops

Fixture recipes use test-only ops such as `echo_activity`, `error-activity`, `test_emit_artifact`, `test_consume_artifact`, `test_write_file`, and `test_read_file`. Use these only in tests or examples that run through the fixture harness.

## Selector Ops

Unknown `op` names that look like selectors are allowed through parser validation and resolved as extension ops later. Prefer explicit `extension_execution` snippets for clarity unless the surrounding docs or examples already use selector shorthand.

## Document Review (`input`)

Use one `op: input` with `inputs.form.kind: review`, ordinary `fields`, and
`form.documents: {document_id: <stored artifact reference>}`. Use `${{ ... }}`
for document values, including producer artifacts or submitted
`context.artifacts["name"]`. No preparation extension, hashes, or wrapper recipe
is required. `form.kind` lets applications recognize reviews.

Read answers from `outputs.fields`; an optional `file_upload` field returns a
stored artifact ref when answered and may be absent otherwise. Bind it through
ordinary downstream `artifacts` after checking presence. `outputs.artifact_refs`
contains returned documents; `outputs.receipt` records acceptance. Decisions
have no built-in workflow meaning. Test reviews may use `form.autofill.fields`.
Do not mix review forms with generic `request` / `response_schema` configuration.

A consuming client must use `SubmitFormResponse` with the pending request ID;
the ordinary CLI prompt does not render document reviews. `--input-mode ops`
exposes their forms. See [the author guide](../../../GUIDE-Review-Recipe-Authors.md).

## Unanswered input

Set `inputs.if_unanswered: {after: 30m, fields: {decision: defer}}` beside `form`
to provide automatic answers after a delay. Use `response` instead of `fields`
for a single question or structured response. This also works with review forms;
documents use existing stored artifact refs. Do not combine it with `form.autofill`.
Templates and answers freeze during preparation, and retries do not reset the
clock. Becoming due does not reject a human answer: the first completion to obtain
authority wins. A worker must be available; hard job/op timeouts still apply.
See [the guide](../../../GUIDE-Input-Fallback-Answers.md) for output and worker details.
