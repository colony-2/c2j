# Recipe-Child Ops User Guide

This guide is for agents configuring recipes that orchestrate child recipes.

## Ops Overview

Use these ops to start child recipes, wait for completion, and fetch outputs/artifacts:

- `recipe.run_and_get_result`: start one child recipe and wait for outputs.
- `recipes.run`: start multiple child recipes and return job IDs immediately.
- `recipes.run_and_wait`: start multiple child recipes and wait for completion (no outputs).
- `recipe.await_result`: wait for a specific child job and fetch outputs.
- `recipe.get_result`: fetch outputs for a child job without waiting.

## Core Inputs

### Single recipe input (used by `recipe.run_and_get_result` and within `recipes.run*`)

```
name: child-recipe-name            # required
inputs:                            # inputs passed to the child recipe
  key: value
artifacts: []                       # optional list of artifact keys to pass to the child job
cell_name: ""                       # optional, defaults from context
cell_path: ""                       # optional, defaults from context
cell: ""                            # optional legacy field (not used when starting jobs)
git:
  base_repo: ""                     # optional, defaults from context
  base_ref: ""                      # optional, defaults from context
  base_hash: ""                     # optional, defaults from context
  author: ""                        # optional, defaults from context
```

When populating `artifacts` with templates, use raw CEL values:

```yaml
artifacts:
  - '${{ sequence.prepare.artifacts["payload.json"] }}'
```

Notes:
- `cell_name` and `cell_path` must be present after defaults are applied; otherwise the op errors.
- `git.base_repo`, `git.base_ref`, and `git.base_hash` must be present after defaults are applied; otherwise the op errors.
- `cell` is accepted but is not used when starting the child job; use `cell_name`/`cell_path`.

### Defaults (used by `recipe.run_and_get_result` and `recipes.run*`)

`defaults` provides shared values for child recipes. If a child recipe omits a field, the default is applied.

```
defaults:
  cell_name: "{{ context.workflow.cell }}"
  cell_path: "{{ context.workflow.cell_path }}"
  git:
    base_repo: "{{ context.git.repo }}"
    base_ref: "{{ context.git.ref }}"
    base_hash: "{{ context.git.resolved_hash }}"
    author: "{{ context.workflow.job_id }}@{{ context.workflow.cell }}"
```

If you omit `defaults`, the op still uses these template defaults internally.

### Output shape

Child outputs are wrapped under `outputs.outputs` in the parent step output:

```
outputs:
  outputs:      # map returned by child recipe outputs
    ...
```

Child artifacts are attached to the parent job output artifacts automatically.
You can reference those as `sequence.<step-id>.artifacts["name"]` in later nodes.

## Ops Reference

### `recipe.run_and_get_result`
Starts a single child recipe and waits for completion.

Inputs:
- Single recipe fields (above)
- `git_ref`: string passed to the child job
- `defaults`: optional defaults object

Outputs:
- `outputs.outputs`: child recipe outputs map

Example:
```yaml
- id: child
  op: recipe.run_and_get_result
  inputs:
    name: child-simple
    inputs:
      value: "{{ inputs.value }}"
    artifacts: []
    git_ref: "{{ inputs.git_ref }}"
```

### `recipes.run`
Starts multiple child recipes and returns job IDs immediately.

Inputs:
- `git_ref`: string passed to all child jobs
- `defaults`: optional defaults applied to all child recipes
- `recipes`: list of single recipe objects

Outputs:
- `job_ids`: list of started job IDs

Example:
```yaml
- id: start
  op: recipes.run
  inputs:
    git_ref: "{{ inputs.git_ref }}"
    recipes:
      - name: child-simple
        inputs:
          value: "one"
        artifacts: []
      - name: child-simple
        inputs:
          value: "two"
        artifacts: []
```

### `recipes.run_and_wait`
Starts multiple child recipes and waits for all to complete. Does not fetch child outputs.

Inputs:
- Same as `recipes.run`

Outputs:
- `job_ids`: list of job IDs (after completion)

Example:
```yaml
- id: start
  op: recipes.run_and_wait
  inputs:
    git_ref: "{{ inputs.git_ref }}"
    recipes:
      - name: child-simple
        inputs:
          value: "hello"
        artifacts: []
```

### `recipe.await_result`
Waits for a single child job to complete, then fetches outputs and artifacts.

Inputs:
- `job_id`: string

Outputs:
- `outputs.outputs`: child recipe outputs map

Example:
```yaml
- id: wait
  op: recipe.await_result
  inputs:
    job_id: "${{ sequence.start.outputs.job_ids[0] }}"
```

### `recipe.get_result`
Fetches outputs and artifacts for a child job without waiting.

Inputs:
- `job_id`: string

Outputs:
- `outputs.outputs`: child recipe outputs map

Notes:
- Errors if the job result is not available yet.

Example:
```yaml
- id: get
  op: recipe.get_result
  inputs:
    job_id: "${{ sequence.start.outputs.job_ids[0] }}"
```

## End-to-End Example

Run a child recipe, pass an artifact into it, then consume a child artifact and output:

```yaml
- id: prepare
  op: command_execution
  inputs:
    working_directory: "{{ context.environment.outbox }}"
    run: "printf 'input for child' > child-input.txt"

- id: child
  op: recipe.run_and_get_result
  inputs:
    name: child-artifact
    inputs: {}
    artifacts:
      - '${{ sequence.prepare.artifacts["child-input.txt"] }}'
    git_ref: "{{ inputs.git_ref }}"

- id: consume_child_artifact
  op: command_execution
  inputs:
    working_directory: "{{ context.environment.inbox }}"
    run: "cat foo"
  artifacts:
    foo: '${{ sequence.child.artifacts["foo"] }}'

outputs:
  child_name: "{{ sequence.child.outputs.outputs.name }}"
```
