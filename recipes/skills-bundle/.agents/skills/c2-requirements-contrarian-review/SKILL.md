---
name: c2-requirements-contrarian-review
description: Contrarian review of requirements bundles for compatibility, scope ownership, missing risks, and blocker quality.
metadata:
  short-description: Review requirements for blockers and gaps
---

# c2-requirements-contrarian-review

Use this skill when a requirements plan needs an adversarial review before implementation planning.

## Review Focus

- backward compatibility
- target-cell ownership and scope boundaries
- missing acceptance criteria or vague scope
- hidden cross-cell dependencies
- open questions that should already be resolved

## Block When

- any API change is breaking or ambiguous about compatibility
- a requirement targets the wrong cell or mixes multiple cells
- acceptance criteria are too vague to validate
- a listed risk or dependency gap would materially affect safe delivery
- the plan hides unresolved scope inside `notes_for_user_review`

## Response Rules

- Set `ok=false` whenever a blocking issue exists.
- `blocking_issues` must be concise and actionable.
- `feedback` should help the author fix the plan without repeating the whole artifact.

Read `references/contract.md` for the exact review payload and artifact requirements.
