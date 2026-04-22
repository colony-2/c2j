---
name: c2-outcome-contrarian-review
description: Contrarian review of outcome plans, test statements, and validation commands for quality, coverage, and compatibility safety.
metadata:
  short-description: Review outcome quality and validation coverage
---

# c2-outcome-contrarian-review

Use this skill when outcome determination needs an adversarial review.

## Review Focus

- statement format and word-count discipline
- positive and negative coverage for critical behavior
- outcome-focus rather than implementation-detail leakage
- alignment with requirements and implementation scope
- validation command sufficiency

## Block When

- statements are not markdown or exceed the 30-word limit
- statements omit filename, importance, type/dependencies, or polarity annotations
- statements do not follow the canonical bullet annotation format
- critical flows lack a negative case
- expectations are backward-incompatible or contradict the plan
- validation commands are missing or obviously insufficient

## Response Rules

- Use `blocking_issues` for exact gaps the author must fix.
- Keep `feedback` concise and outcome-oriented.
- Approve only when a reviewer could realistically validate the ticket from the artifacts.

Read `references/contract.md` for the exact response and artifact contract.
