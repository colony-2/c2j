# Child Group User Guide

`child_group` is a recipe node for starting multiple child recipes from one recipe step.

It is useful when a parent recipe needs to fan out work to several cells or checks, then continue with a single aggregate result.

## Basic Shape

```yaml
sequence:
  - id: checks
    child_group:
      mode: run_and_get_result
      children:
        - key: lint
          recipe: lint-recipe
          required: true
          inputs:
            target: api
        - key: docs
          recipe: docs-recipe
          required: false
          inputs:
            target: docs
      aggregate:
        shape: review_pack

outputs:
  ok: "${{ sequence.checks.outputs.ok }}"
  failed: "${{ sequence.checks.outputs.summary.failed }}"
```

Each child has:

- `key`: stable child name inside the group.
- `recipe`: child recipe id or selector.
- `inputs`: input map passed to that child recipe.
- `required`: defaults to `true`; failed required children set group `ok` to `false`, while failed optional children become warnings.
- `when`: optional CEL condition. If false, the child is skipped.
- `skip_reason`: optional text recorded when a child is skipped.
- `cell_name`, `git_ref`, `artifacts`: optional child invocation overrides.

## Dynamic Children

Use `children_from` with a `child` template to create children from a list. The template can reference `item` and `index`.

```yaml
sequence:
  - id: fanout
    vars:
      targets:
        - key: api
          recipe: test-api
        - key: web
          recipe: test-web
    child_group:
      mode: run_and_get_result
      children_from: "${{ vars.targets }}"
      child:
        key: "${{ item.key }}"
        recipe: "${{ item.recipe }}"
        inputs:
          target: "${{ item.key }}"
```

## Modes

- `run_and_get_result`: starts children, waits for them, collects outputs, and returns a full aggregate result. This is the default.
- `start`: starts children and returns job ids without waiting.

## Outputs

The group output includes:

- `ok`: true when all required children started and completed successfully.
- `child_job_ids`, `required_child_job_ids`, `optional_child_job_ids`, `failed_child_job_ids`.
- `children`: ordered child records with `key`, `recipe`, `required`, `status`, `job_id`, `outputs`, and `error`.
- `summary`: counts for `total`, `started`, `completed`, `failed`, `failed_required`, `failed_optional`, `skipped`, `start_failed`, `required`, and `optional`.
- `warnings` and `blocking_issues`.
- `aggregate`: shape-specific aggregate data.

## Aggregate Shapes

- `none`: no aggregate payload beyond the standard output fields.
- `job_ids`: returns an `aggregate.jobs` list.
- `review_pack`: returns child summaries plus `blocking_issues` and `warnings`.

You can also write the aggregate payload as an output artifact:

```yaml
child_group:
  mode: run_and_get_result
  children:
    - key: lint
      recipe: lint-recipe
  aggregate:
    shape: review_pack
    artifact: review-pack.json
```

## Failure Behavior

Child recipe failures are soft at the group level. The `child_group` node itself succeeds and reports failures in `children`, `summary`, `warnings`, and `blocking_issues`.

Use `required: true` for children that should make `ok` false when they fail. Use `required: false` for advisory children that should not block the parent recipe.

For `review_pack`, blocking issues from required children become group `blocking_issues`. Blocking issues from optional children are downgraded to group `warnings`.

## Artifact Inputs

`artifacts.use` passes artifacts to every child. Entries can be full artifact refs or a string key from `context.artifacts`.

```yaml
child_group:
  artifacts:
    use:
      - ticket-intake
  children:
    - key: review
      recipe: review-ticket
```
