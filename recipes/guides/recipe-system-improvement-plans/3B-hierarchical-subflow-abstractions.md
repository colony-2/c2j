# Plan 3B: Hierarchical Subflow Abstractions for Drill-Down Reviews

## Goal

Support explicit high/low-resolution authoring and review by introducing executable hierarchical flow nodes.

## Problem pattern in current recipes

Large recipes combine orchestration and detailed mechanics in one flat state machine. This is hard to audit at the right level for different reviewers.

## Proposal

Add a `subflow` node abstraction:

- behaves like a nested recipe/state-machine block,
- appears as one node at high level,
- can be expanded for detailed review,
- preserves inherited git/artifact context unless explicitly overridden.

## Proposed syntax

```yaml
state:
  states:
    implementation_phase:
      subflow:
        source: internal://subflows/codex-implementation-loop@v1
        inputs:
          title: "{{ inputs.title }}"
        inherit_context:
          git_state: true
          artifacts: true
        expose:
          outputs:
            - validation_ready
            - pending_bug_ticket_specs
```

## Recipe changes expected

`new-ticket.yaml` can be refactored into:

1. triage subflow
2. planning subflow
3. implementation subflow
4. merge/completion subflow

Each subflow remains drillable for low-level review and tests.

## Engine/runtime changes

1. Add `subflow` schema/node type.
2. Implement subflow execution with clear scope boundaries.
3. Extend job story with path mapping:
   - `parent_node_path`
   - `subflow_node_path`
   - `expanded_execution_path`.
4. Ensure inherited git/artifact behavior is deterministic.

## Migration plan

1. Start with same-recipe embedded subflows (no remote references).
2. Add referenced reusable subflow source support.
3. Migrate `new-ticket.yaml` in phases with parity tests.

## Compatibility and risks

- Compatibility: moderate (new node type affects schema/tooling).
- Risk: scope confusion if subflow visibility rules are unclear.
- Mitigation:
  - strict scope spec update
  - explicit `expose.outputs` contract
  - tooling command to print expanded effective graph.

## Success criteria

1. High-level review can focus on 5–8 nodes for full ticket lifecycle.
2. Drill-down preserves exact executable detail and traceability.
3. Existing guarantees (single-cell commit, artifact handoff, ticket lifecycle) remain intact.
