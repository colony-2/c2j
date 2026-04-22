---
name: c2-requirements-author
description: Author a compatible, cell-scoped requirements bundle that turns a ticket into testable outcomes and explicit dependency candidates.
metadata:
  short-description: Write testable c2 requirements bundles
---

# c2-requirements-author

Use this skill when a ticket has passed triage and needs an author-quality requirements plan.

## Workflow

1. Read the ticket, current cell, available cells, and any prior feedback.
2. Decompose the work into stable requirement units.
3. Keep the plan outcome-focused, not implementation-prescriptive.
4. Write the required artifacts and return only the exact `plan.json` object.

## Authoring Rules

- Use stable IDs like `REQ-1`, `REQ-2`, in dependency order.
- Keep each requirement scoped to a single owning cell.
- Set `needs_cross_cell_support=true` only when another cell must change before the ticket can fully land.
- Put answered details into `scope` or `acceptance_criteria`, not `open_questions`.
- Keep API changes backward compatible. If no API change is needed, use an empty list.
- Acceptance criteria must describe observable outcomes.
- Risks should capture realistic failure modes, compatibility gaps, or rollout concerns.
- Do not smuggle local implementation steps into requirements.

## Quality Bar

- The plan summary states the user-visible outcome and compatibility stance.
- Every requirement is testable and materially different from the others.
- Cross-cell dependencies are explicit, not implied.
- Open questions are rare and only for unresolved blockers.

Read `references/contract.md` for the exact file set and JSON schema.
