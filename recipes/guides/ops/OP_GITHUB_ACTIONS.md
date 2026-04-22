# GitHub Actions Ops

This guide covers:

- `gha.run`: run one workflow and return normalized workflow output.
- `gha.runs`: run multiple workflows in parallel and return one result per workflow.

Use these ops when a recipe should reuse an existing repository workflow instead of re-implementing the same automation in shell or agent code.

## What These Ops Do Not Do

- They do not expose job-level selection. There is no `gha.run_job` op.
- They do not let recipes choose a GitHub event name. Both backends execute as `workflow_dispatch`.
- They do not persist workflow-driven file or git mutations back into recipe git state. All workflow worktrees are disposable.
- They do not run workflows from other repositories or cell-relative paths.

## Workflow Selection

`workflow` is a required file name such as `ci.yml` or `release.yaml`.

The op always resolves it to:

```text
.github/workflows/<workflow>
```

Rules:

- The value must be a file name only. No `/` or `\` separators are allowed.
- The value must end in `.yml` or `.yaml`.
- The file must exist in the current repository at `.github/workflows/<workflow>`.
- The selected workflow must declare `on.workflow_dispatch`.
- Subdirectories under `.github/workflows` are not supported.
- Protocol prefixes like `repo://`, `cell://`, and `git+...` are not supported.

## Shared Inputs

Both ops use this base workflow input:

```yaml
workflow: "ci.yml"
with:
  target: "api"
env:
  CI: "true"
secrets:
  GITHUB_TOKEN: "{{ inputs.github_token }}"
backend: "local"
runner_image: "ghcr.io/catthehacker/ubuntu:act-latest"
timeout: "20m"
continue_on_error: false
remote:
  push_to: "origin"
  ref_prefix: "c2/gha"
```

Field notes:

- `workflow`: required workflow file name under `.github/workflows/`.
- `with`: workflow-dispatch inputs.
- `env`: extra environment variables for the workflow run.
- `secrets`: workflow secrets. `secrets.GITHUB_TOKEN` is required for `backend: github`.
- `backend`: `local` or `github`. Defaults to `local`.
- `runner_image`: overrides the Docker image used by the local backend.
- `timeout`: Go duration string such as `5m` or `30m`.
- `continue_on_error`: when `false`, a non-success workflow fails the activity. Failed structured output is still recorded with the activity failure.
- `remote.push_to`: optional remote name or URL for `backend: github`. If omitted, the op derives `git@github.com:<base_repo>.git` from git context.
- `remote.ref_prefix`: optional temp branch prefix for `backend: github`. Defaults to `c2/gha`.

## Backend Behavior

### `backend: local`

This is the default backend. It runs the workflow locally through `nektos/act` and requires a working Docker daemon.

Important details:

- The workflow is planned and executed as `workflow_dispatch`.
- The current recipe worktree is cloned first, and the workflow runs against that disposable clone.
- All filesystem mutations made by the workflow are discarded after the run.
- `with` values are stringified before they are passed into `act`.
- Logs and artifacts are exposed as local external artifacts.

### `backend: github`

This backend dispatches the workflow remotely through the GitHub Actions API.

What it does:

1. Pushes the current local HEAD to a temporary branch.
2. Dispatches the workflow by file name using `workflow_dispatch` against that temp branch.
3. Polls the workflow run, jobs, logs, and artifacts.
4. Deletes the temporary branch.

Important details:

- `secrets.GITHUB_TOKEN` is required.
- The workflow must support `workflow_dispatch`.
- The same workflow path must exist on the repository default branch so GitHub will accept the dispatch.
- The run executes the selected temp-branch copy of the workflow, not the default-branch copy.
- `remote.push_to` can be a git remote name like `origin`, an SSH URL, or an HTTPS URL.
- Any commits or pushes performed by the workflow stay isolated to the temporary branch and are discarded when that branch is deleted.
- No workflow changes are synced back into the recipe worktree.

## `gha.run`

Runs one workflow and returns workflow-level output.

Example:

```yaml
- id: ci
  op: gha.run
  inputs:
    workflow: "ci.yml"
    backend: "local"
    with:
      target: "api"
```

Example output:

```json
{
  "status": "success",
  "exit_code": 0,
  "duration_seconds": 42,
  "workflow": {
    "resolved_selector": "ci.yml",
    "resolved_commit": "deadbeef",
    "content_hash": "sha256:..."
  },
  "jobs": {
    "lint": {
      "name": "lint",
      "status": "success",
      "conclusion": "success",
      "duration_seconds": 18,
      "steps": [
        {
          "name": "Run lint",
          "status": "success",
          "conclusion": "success",
          "duration_seconds": 12
        }
      ]
    }
  }
}
```

## `gha.runs`

Runs several workflows in parallel against isolated clones of the current worktree.

Example:

```yaml
- id: checks
  op: gha.runs
  inputs:
    timeout: "15m"
    continue_on_error: true
    workflows:
      - id: lint
        workflow: "lint.yml"
        backend: "local"
      - id: docs
        workflow: "docs.yml"
        backend: "local"
```

Example output:

```json
{
  "status": "failure",
  "all_passed": false,
  "results": {
    "lint": {
      "status": "success",
      "exit_code": 0
    },
    "docs": {
      "status": "failure",
      "exit_code": 1,
      "error_message": "workflow concluded with status failure"
    }
  }
}
```

Batch behavior:

- Each workflow runs in its own disposable cloned worktree.
- Artifacts are prefixed by workflow key, for example `lint/gha-logs` or `docs/test-results`.
- The workflow key comes from `workflows[].id` when present, otherwise from the workflow file name.
- Mutations are discarded for every workflow entry.

## Artifacts And Logs

These ops register external artifacts that later steps can consume.

Common artifact keys:

- `gha-logs`: combined workflow logs
- `gha-logs/<job>`: per-job logs when available
- `<artifact-name>`: workflow artifact name after sanitization

Backend differences:

- `backend: local` stores logs and artifacts locally and exposes them as file-backed external artifacts.
- `backend: github` exposes GitHub-hosted log and artifact download URLs.

With `gha.runs`, each artifact key is prefixed with the workflow key:

- `lint/gha-logs`
- `lint/test-results`
- `docs/gha-logs`

## Status Model

Normalized statuses are:

- `success`
- `failure`
- `cancelled`
- `timed_out`

`exit_code` is `0` for success and `1` for non-success outcomes.

## Practical Guidance

- Use `backend: local` for fast validation when Docker is available.
- Use `backend: github` when the workflow depends on real GitHub-hosted runners, repository permissions, or hosted Actions services.
- Keep reusable automation in `.github/workflows/` and make sure any workflow you want to call through `gha` declares `workflow_dispatch`.
- Make workflows expose results through logs, workflow artifacts, or explicit outputs. Do not rely on filesystem mutations persisting after the op completes.
