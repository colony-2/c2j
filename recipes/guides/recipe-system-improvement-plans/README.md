# Recipe System Improvement Proposals (2026-02-24)

## Why this set exists

Current recipe authoring works, but large workflows (for example `new-ticket.yaml`) show three recurring pain points:

1. CEL/state expressions are verbose and repetitive.
2. Repeated workflow sub-patterns are copied instead of encapsulated.
3. Human reviewers need both high-level and low-level flow views, but recipes expose mostly the low-level view.

This folder proposes alternatives for each area, with concrete recipe/runtime changes.

## Alternatives index

### 1) Cleaner state/CEL expressions

- `1A-cel-null-safety-profile.md`
  - Add null-safe CEL behavior (optional profile) so missing state fields evaluate to null instead of errors.
- `1B-state-access-helpers-and-locals.md`
  - Keep CEL strict, add helper functions + reusable `locals` to reduce repetition.

### 2) Encapsulate repetitive sub-patterns

- `2A-recipe-fragments-and-composition.md`
  - Introduce reusable recipe fragments (expand to nodes/transitions at compile time).
- `2B-composite-ops-for-common-workflow-loops.md`
  - Introduce higher-level ops that bundle common loops (review gates, codex loop, ticket close/cancel).

### 3) Multi-resolution flow review

- `3A-multi-resolution-recipe-lenses.md`
  - Add non-executable metadata + CLI lenses to collapse/expand flow views.
- `3B-hierarchical-subflow-abstractions.md`
  - Add explicit subflow nodes with drill-down semantics and inherited git/artifact context.

### 4) Skill-oriented checkpoint model

- `SKILL_ALTERNATIVES_OVERVIEW.md`
  - Index of skill-driven alternatives while preserving checkpoint visibility.
- `4A-skill-aware-codex-exec.md`
  - Make `codex.exec` skill-aware with structured progress/status contracts.
- `4B-checkpoint-centric-recipe-refactor.md`
  - Keep major checkpoints in recipe; move implementation internals into skills.
- `4C-skill-packaging-and-governance.md`
  - Versioning/testing/governance model for skill packs used by recipes.
- `4D-stepwise-codex-checkpointing-requirements.md`
  - Requirements for per-step Codex process model with session resume and checkpoint returns.
- `4E-codex-exec-user-api-and-multi-skill-examples.md`
  - Concrete `codex.exec` user API changes and multi-skill stepwise examples.
- `4F-multi-git-ref-skill-sources-for-codex-exec.md`
  - Multi-source git-ref pattern for platform skill directories in a single `codex.exec` call.

## Suggested rollout order (lowest risk first)

1. Implement `1B` (helpers/locals) + `3A` (lenses) first.
2. Add `2A` fragments after helpers stabilize.
3. Re-evaluate `1A`, `2B`, `3B` as larger engine changes once the low-risk layer is proven.

## Clarifying questions for prioritization

1. Do you prefer keeping CEL strict by default (`1B`) or enabling optional null-safe profile (`1A`) quickly?
2. For pattern reuse, is your preference transparent compile-time expansion (`2A`) over opaque higher-level ops (`2B`)?
3. For flow review, should first delivery be read-only visualization (`3A`) before any executable hierarchy (`3B`)?
4. Should these ship as experimental flags first (`--experimental-*`) or as first-class defaults?
5. Do you want one pilot migration on `new-ticket.yaml` as acceptance criteria for phase 1?
