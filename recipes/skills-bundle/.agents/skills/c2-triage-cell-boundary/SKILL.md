---
name: c2-triage-cell-boundary
description: Decide whether a ticket belongs in the current c2 cell or should be reassigned to a better matching available cell.
metadata:
  short-description: Evaluate ticket-to-cell fit
---

# c2-triage-cell-boundary

Use this skill when a recipe needs a durable decision about whether work belongs in the current cell.

## Workflow

1. Read the ticket title, description, current cell, and available cells.
2. Compare ownership, specialization, and likely code-touch surface.
3. Choose exactly one outcome:
   - keep work in the current cell
   - reassign to one provided cell name
4. Write the required artifacts and return only the exact JSON payload.

## Decision Rules

- Prefer the most specialized matching cell over a generic cell.
- Keep the work in the current cell when no alternative is clearly better.
- Never invent a cell name or recommend more than one cell.
- Base the rationale on the actual ownership boundary, not generic wording.
- Treat recipe, workflow, testing, and process-document changes as `recipe-tests` work unless another cell clearly owns the affected code.
- Treat obvious UI styling or component work as `frontend` work.
- Treat deployment, provisioning, and runtime platform changes as `infra` work.

## Quality Bar

- The rationale should mention the winning scope reason.
- When reassigning, explain why the current cell is a worse fit.
- When staying in-cell due to ambiguity, say what is ambiguous.

Read `references/contract.md` for the exact artifact paths and response fields.
