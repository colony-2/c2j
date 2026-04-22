# Plan 4C: Skill Packaging, Testing, and Governance for Recipe Workflows

## Goal

Prevent hidden behavior drift when recipes depend on skills for inner-loop execution.

## Proposed model

## 1) Skill packs as versioned artifacts

Each recipe phase references explicit skill pack versions:

- `c2-requirements-pack@v1`
- `c2-implementation-pack@v3`
- `c2-merge-pack@v1`

Skill pack manifest format:

```json
{
  "name": "c2-implementation-pack",
  "version": "v3",
  "skills": ["c2-implementation-loop@v1", "c2-cross-cell-bug-reporter@v1"],
  "contracts": {
    "required_artifacts": [
      "implementation/latest-status.json",
      "implementation/progress.ndjson"
    ]
  }
}
```

## 2) Skill contract tests in recipe test framework

Add test mode to validate skill-produced artifacts against schema:

- `assertion type: artifact_schema_matches`
- `assertion type: status_transition_allowed`

This keeps recipe behavior deterministic even when skill internals evolve.

## 3) Policy controls

Per project/cell policy:

- allow-listed skills/packs only.
- optional `enforce_pinned_versions`.
- optional `require_review_for_skill_pack_change`.

## 4) Review/audit visibility

Job story should include:

- resolved skill pack versions used,
- missing/fallback skill events,
- contract validation pass/fail details.

## Recipe authoring changes

Recipes declare required packs, not ad hoc skills in prompts:

```yaml
inputs:
  skill_pack: c2-implementation-pack@v3
```

Engine resolves pack to concrete skills.

## Migration plan

1. Define pack manifest spec and validation.
2. Create baseline pack for `new-ticket` implementation phase.
3. Add recipe tests for artifact contract compliance.
4. Enable policy enforcement after pilot confidence.

## Risks and mitigations

- Risk: governance overhead slows iteration.
  - Mitigation: allow `@next` channels in non-prod projects.
- Risk: too many packs.
  - Mitigation: standardize on small set of canonical packs per lifecycle phase.

## Success criteria

1. Skill-driven workflows remain reproducible and auditable.
2. Recipe maintainers can upgrade skill behavior via controlled version bumps.
3. Checkpoint outcomes remain stable across skill pack updates.
