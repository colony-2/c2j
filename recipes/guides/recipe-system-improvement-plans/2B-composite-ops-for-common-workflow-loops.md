# Plan 2B: Composite Ops for Common Workflow Loops

## Goal

Reduce repeated YAML by moving common multi-node orchestration into higher-level ops.

## Problem pattern in current recipes

Some patterns are business-stable but structurally repetitive:

- implementation loop with Codex session resume.
- review gate with structured decision options.
- ticket cancellation and completion updates.

## Proposal

Add higher-level ops that encode these loops directly, with explicit structured inputs/outputs.

## Candidate composite ops

1. `workflow.review_gate`
   - Input: checkpoint summary blocks, decision options, default routing.
   - Output: decision + feedback + target stage.
2. `workflow.codex_loop`
   - Input: prompt, artifact mappings, bug-ticket rules, question handling policy.
   - Output: terminal status, created bug ticket specs, follow-up reason/category.
3. `ticket.finalize`
   - Input: mode (`done`/`canceled`), note template, actor ticket id.
   - Output: ticket update result.

## Recipe changes expected

`new-ticket.yaml` can shrink to phase orchestration:

- planning sequence
- outcome sequence
- `workflow.codex_loop`
- validate
- merge/complete.

This removes most repeated support states.

## Engine/runtime changes

1. Implement composite ops with full story emission of internal steps.
2. Add structured output schema for each composite op.
3. Add testing helpers/mocks to target internal branches deterministically.

## Migration plan

1. Start with `ticket.finalize` (lowest complexity).
2. Introduce `workflow.review_gate`.
3. Introduce `workflow.codex_loop` last.
4. Migrate `new-ticket` incrementally with behavior parity tests.

## Compatibility and risks

- Compatibility: moderate; fewer explicit nodes may impact existing node-path-based test fixtures.
- Risk: over-abstraction can hide policy details and reduce local customization.
- Mitigation:
  - story includes internal pseudo-node trace
  - configuration hooks for decision options/labels
  - preserve opt-in use; existing recipes continue working.

## Success criteria

1. New recipes can implement full ticket flow with substantially fewer states.
2. Existing policy requirements (bug ticket creation, user clarification loop, test statement constraints) remain expressible.
3. Job story still provides enough detail for audit/debug.
