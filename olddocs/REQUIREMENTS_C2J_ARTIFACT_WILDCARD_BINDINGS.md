# Requirements: Artifact Wildcard Inbox Bindings

## Problem

Recipes can bind individual artifact refs into an op inbox, but generic recipes often need to pass an entire artifact set through without knowing artifact names in advance.

This is especially important for:

- job input artifacts submitted with `c2j submit --artifact`, exposed as `context.artifacts`
- artifacts produced by another node, exposed as `sequence.<node>.artifacts` or `states.<state>.artifacts`
- child recipe outputs where a parent recipe wants to forward a whole artifact bundle

Today, authors must enumerate every artifact name:

```yaml
artifacts:
  brief.md: '${{ context.artifacts["brief.md"] }}'
  requirements/plan.json: '${{ states.requirements.artifacts["requirements/plan.json"] }}'
```

That does not work for open-ended submit attachments or variable output bundles.

## Requirements

1. Recipes must support binding every artifact in an artifact set into an op inbox without statically listing each name.
2. The feature must work for any artifact set available to templates, including `context.artifacts`, `sequence.<node>.artifacts`, and `states.<state>.artifacts`.
3. Default behavior should preserve each artifact's existing name as the inbox-relative path.
4. Binding names must keep the same path safety rules as explicit `artifacts:` keys.
5. Collisions must be deterministic and visible, either by failing with a clear error or by requiring an explicit collision policy.
6. Authors should be able to combine wildcard bindings with explicit bindings in the same op.
7. Authors should be able to place a wildcard-bound set under an inbox prefix when needed.
8. The feature must not read or embed artifact contents during template evaluation. It should only pass artifact refs to the op executor.

## Possible Syntax Options

Allow `artifacts:` to accept a template expression that resolves to a map:

```yaml
artifacts: '${{ context.artifacts }}'
```

Add a wildcard key convention:

```yaml
artifacts:
  "**": '${{ context.artifacts }}'
  requirements/**: '${{ states.requirements.artifacts }}'
```

Add an explicit helper that converts an artifact set to inbox bindings:

```yaml
artifacts: '${{ artifact_bindings(context.artifacts) }}'
```

Support prefixing through a helper or option:

```yaml
artifacts: '${{ artifact_bindings(states.requirements.artifacts, {"prefix": "requirements-input/"}) }}'
```

These examples are illustrative. The implementation should choose the syntax that best fits c2j's recipe parser and template model.

## Acceptance Criteria

- A recipe can bind all `context.artifacts` into a command op inbox and read the submitted files by their submitted names.
- A recipe can bind all artifacts from `sequence.prepare.artifacts` into a later op inbox.
- A recipe can bind all artifacts from `states.plan.artifacts` into a later state op inbox.
- Explicit bindings and wildcard bindings can be used together in one op.
- Duplicate inbox paths fail with a clear error unless an explicit collision policy is provided.
- Existing explicit artifact bindings keep working unchanged.

