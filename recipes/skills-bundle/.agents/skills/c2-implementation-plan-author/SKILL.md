---
name: c2-implementation-plan-author
description: Translate approved requirements into a safe implementation plan with explicit local steps and dependency ticket specs for other cells.
metadata:
  short-description: Turn requirements into safe execution plans
---

# c2-implementation-plan-author

Use this skill when approved requirements need to become an execution plan for implementation.

## Workflow

1. Read the approved requirements plan and summaries.
2. Separate local work from cross-cell prerequisites.
3. Write an ordered plan that preserves compatibility.
4. Return only the exact `plan.json` object and required artifacts.

## Authoring Rules

- `local_steps` should be concrete, executable, and scoped to the current cell.
- `dependency_ticket_specs` should exist only for requirements owned by other cells.
- Set `requires_dependency_tickets=true` only when at least one external prerequisite exists.
- Preserve requirement dependency order in `dependency_order`.
- Keep rollout and validation concerns explicit.
- Do not plan direct edits in other cells.
- Do not introduce or normalize backward-incompatible changes.

## Quality Bar

- The summary describes how the work lands safely.
- Local steps are small enough to execute without guesswork.
- Cross-cell asks are specific enough to become tickets.
- Notes for user review call out tradeoffs or sequencing risk, not implementation minutiae.

Read `references/contract.md` for the exact schema and file requirements.
