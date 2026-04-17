# Codex Skill-Aware Op Implementation Plan

## Goal

Implement `codex.exec` behavior that supports skills as first-class execution units while keeping orchestration decisions in the op/recipe runtime, not with end users.

## Outcomes We Need

1. Codex supports skills.
2. One `codex.exec` invocation executes exactly one top-level skill segment.
3. The op decides what happens next based on structured state.
4. The op returns when specific behaviors require external handling (user input, other cell work, dependency tickets, etc.).
5. Session resume remains reliable across fresh processes, including flows where skills trigger nested skills internally.

## Design Principles

1. Artifact-first routing: authoritative state comes from status artifacts.
2. Op-owned control flow: skill outputs inform decisions, but op logic chooses continue/return behavior.
3. Backward compatibility: existing `codex.exec` calls without skills continue to work.
4. Determinism: skill selection and status contracts should be reproducible and auditable.
5. Single combined output shape: one consistent response object with both human and machine fields.

## Proposed Op Contract

### Inputs

- `skills: string[]`
- `skill_mode: enforce`
- `skill_selection_mode: adaptive | ordered` (default `adaptive`)
- `selected_skill: string` (optional explicit override for this invocation)
- `return_on: string[]`
- `status_contract.path: string` (artifact path to authoritative status)
- `resume_context` (optional nested-checkpoint context from prior invocation)

### Outputs

- `status`
- `sessionId`
- `outcome` (single combined object)

Suggested `outcome` shape:

```json
{
  "summary": {
    "human": "Short human-readable summary",
    "reason": "optional machine-readable summary reason"
  },
  "skill": {
    "executed": "c2-implementation-loop@v1",
    "selection_mode": "adaptive",
    "next_candidates": ["c2-validation-runner-fixer@v1"]
  },
  "checkpoint": {
    "status": "needs_user_input",
    "return_triggered": true,
    "return_reason": "needs_user_input",
    "status_artifact": "implementation/latest-status.json",
    "contract_errors": [],
    "scope": "top_level",
    "stack": [
      {"skill": "parent-checkpoint", "scope": "top_level"}
    ]
  },
  "routing": {
    "next_action": "return_to_recipe_checkpoint"
  }
}
```

Notes:

1. This replaces fragmented output fields with one stable structure.
2. If needed for migration, `assistantSummary` can be emitted as a temporary alias of `outcome.summary.human`.

## Nested Checkpoint Contract

Nested skills are treated as first-class checkpoints.

Required behavior:

1. If a nested skill hits a checkpoint condition, the invocation must return.
2. Return payload must identify nested checkpoint scope and blocking skill.
3. Resume must preserve checkpoint stack so continuation can re-enter correctly.

Suggested nested checkpoint extension:

```json
{
  "checkpoint": {
    "status": "needs_user_input",
    "scope": "nested",
    "blocking_skill": "child-checkpoint",
    "stack": [
      {"skill": "parent-checkpoint", "scope": "top_level"},
      {"skill": "child-checkpoint", "scope": "nested"}
    ],
    "status_artifact": "outbox/child/latest-status.json"
  }
}
```

Suggested `resume_context` shape:

```json
{
  "checkpoint_stack": [
    {"skill": "parent-checkpoint", "scope": "top_level"},
    {"skill": "child-checkpoint", "scope": "nested"}
  ],
  "resume_from_status_artifact": "outbox/child/latest-status.json"
}
```

## Single-Skill Execution Model

1. Start invocation with declared skill set and execution settings.
2. Select one top-level skill (`selected_skill` if provided, otherwise op-selected).
3. Execute exactly one skill segment.
4. Read and validate `status_contract.path`.
5. Evaluate return conditions and compute routing decision.
6. Return with `outcome` payload.

Important:

1. "One skill per invocation" applies to top-level op orchestration.
2. Nested skill behavior inside Codex is allowed, but treated as internal to the one executed segment.
3. Any nested checkpoint condition is promoted to an op-level return.

## Return-On Conditions

Standardize machine-routable statuses:

- `needs_user_input`
- `needs_other_cell_changes`
- `needs_dependency_tickets`
- `needs_test_statement_update`
- `blocked`
- `ready_for_validation`
- `ready_for_merge`
- `checkpoint_ready`

Rules:

1. If a status in `return_on` is emitted, return immediately.
2. For condition-specific statuses, required artifacts must exist.
3. Missing required artifacts convert outcome to `blocked` with `contract_errors`.
4. Nested checkpoint statuses are evaluated the same way as top-level statuses.

## Op-Owned Next-Step Decisioning

Decision order:

1. Validate status artifact against contract.
2. Apply return-on policy.
3. If not returning for a blocking/checkpoint reason, compute next skill recommendation:
- `adaptive` (default): choose from `next_skill_candidates` constrained to allowed skills
- `ordered`: choose next item in configured sequence
4. Set `outcome.routing.next_action` and return.
5. If no valid next skill exists, return `blocked` with diagnostic.

Nested rule:

1. If `checkpoint.scope == nested`, `outcome.routing.next_action` must still be `return_to_recipe_checkpoint`.
2. Recipe checkpoint can branch on `scope` to select nested-resolution UX or standard checkpoint UX.

## Skill Storage Strategy

Recommended model: source-of-truth + runtime materialization.

1. Source-of-truth:
- versioned skills and manifests in repo
- pinned by recipe/op inputs
2. Runtime:
- materialize resolved skills into per-job workspace (for example `<workdir>/.codex/skills`)
- set `CODEX_HOME` to workspace-local path for deterministic runtime behavior

Why:

1. Reproducible across machines and environments.
2. Compatible with sandboxed execution constraints.
3. Avoids drift from host-global user skill directories.

## Governance and Safety

1. `skill_mode=enforce` fails when required skills are missing/invalid.
2. Add allowlist support by project/cell.
3. Require explicit version pins for production recipes.
4. Emit audit fields:
- resolved skill versions
- contract validation failures

## Phased Delivery Plan

### Phase 1: Direct Codex Prototyping (Validation First)

1. Build a prototype harness that runs direct `codex exec` with representative skills.
2. Validate one-skill-per-invocation behavior with fresh-process resume via `sessionId`.
3. Validate nested-skill scenarios and confirm resumability and routing consistency.
4. Validate required return behaviors (`needs_user_input`, `needs_other_cell_changes`, etc.).
5. Capture constraints/edge cases and lock the final output contract before op implementation.

Prototype acceptance criteria:

1. Nested checkpoint state is artifact-backed and machine-routable.
2. Invocation returns when nested checkpoint triggers.
3. Resume with preserved stack continues correctly in a fresh process.
4. No routing logic depends on freeform assistant text.

### Phase 2: Contract + Parsing Foundations

1. Add input contract fields (`skills`, `skill_mode=enforce`, selection mode, return policy).
2. Implement single combined `outcome` output shape.
3. Add status artifact parsing and contract validation.
4. Preserve backward-compatible no-skill path.

### Phase 3: Single-Skill Orchestration and Routing

1. Implement one top-level skill execution per invocation.
2. Implement adaptive-default next-skill recommendation.
3. Implement return-on evaluator and routing action emission.
4. Add tests for early return and contract failures.

### Phase 4: Storage + Governance

1. Implement runtime skill materialization.
2. Add version pin and allowlist checks.
3. Add audit visibility for resolved skill set and policy outcomes.

### Phase 5: Pilot and Rollout

1. Migrate one recipe path to skill-aware op flow.
2. Compare checkpoint parity and routing determinism.
3. Roll out gradually behind a feature flag.

## Test Plan

1. Unit tests:
- contract validation
- return-on evaluator
- skill selection (adaptive default, ordered override)
- missing artifact handling
2. Integration tests:
- single-skill invocation with early return
- nested-skill scenario with session resume
- resume behavior with `sessionId`
- deterministic routing from artifacts
3. Regression tests:
- legacy `codex.exec` behavior without `skills`

## Success Criteria

1. Skill-enabled workflows run with deterministic artifact-based routing.
2. Op controls continuation and return behavior without user-driven orchestration.
3. Required return-on cases reliably hand control back to recipe checkpoints.
4. Existing non-skill recipes remain unchanged.
