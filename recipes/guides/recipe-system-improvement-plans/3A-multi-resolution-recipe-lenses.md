# Plan 3A: Multi-Resolution Recipe Lenses (Metadata + CLI Views)

## Goal

Let humans review recipe flows at different levels without changing execution semantics.

## Problem pattern in current recipes

Current flow graphs are detailed and accurate, but large recipes are hard to review quickly because all low-level states are shown at once.

## Proposal

Add non-executable metadata to nodes and provide lens-based rendering in CLI/UI.

## Metadata model

Add optional node metadata:

```yaml
meta:
  phase: requirements | planning | outcome | implementation | merge
  checkpoint: pre_implementation | ready_to_merge
  abstraction_group: implementation_loop
  review_label: "Pre-implementation checkpoint"
```

No runtime behavior changes; metadata is for tooling only.

## CLI/UI enhancements

1. `c2 recipe graph --lens phase`
   - collapses nodes into phase blocks.
2. `c2 recipe graph --lens checkpoints`
   - highlights human input gates and major decision paths.
3. `c2 recipe graph --expand <group>`
   - expands only selected abstraction groups.
4. `c2 recipe doc generate --view high|medium|full`
   - emits markdown/mermaid docs automatically.

## Recipe changes expected

- Add `meta` tags to existing states in:
  - `new-ticket.yaml`
  - child planning/outcome recipes.
- Maintain current node structure and tests.

## Engine/tooling changes

1. Extend schema to allow `meta` on nodes.
2. Add graph renderer that can collapse by `phase` or `abstraction_group`.
3. Keep canonical full graph as source of truth.

## Migration plan

1. Add schema + no-op parser support.
2. Tag `new-ticket.yaml` as pilot.
3. Implement `graph --lens phase`.
4. Add checkpoints and expansion controls.

## Compatibility and risks

- Compatibility: very high (metadata-only).
- Risk: inconsistent tagging across recipes.
- Mitigation: lint rules requiring known `phase` values and coverage checks.

## Success criteria

1. One-command high-level flow diagram generation for any recipe.
2. Reviewers can move from phase view to state detail with stable node mapping.
3. No change to existing runtime behavior/tests.
