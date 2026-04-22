---
name: c2-implementation-compat-review
description: Contrarian review of implementation plans for compatibility, sequencing safety, and proper cross-cell dependency handling.
metadata:
  short-description: Review implementation plans for safety
---

# c2-implementation-compat-review

Use this skill when an implementation plan needs an adversarial compatibility and sequencing review.

## Review Focus

- backward compatibility and rollout safety
- whether other-cell work is represented as dependency tickets instead of direct changes
- dependency sequencing correctness
- whether local steps are sufficient to land the current-cell work safely

## Block When

- the plan hides breaking changes or risky migrations
- cross-cell work is assigned to local steps
- `requires_dependency_tickets` is false even though external prerequisites exist
- dependency tickets target the current cell or omit essential scope
- validation or test updates are missing from a risky plan

## Response Rules

- Block aggressively on compatibility or sequencing ambiguity.
- Keep `blocking_issues` specific and repairable.
- Use `feedback` to summarize the safest correction path.

Read `references/contract.md` for the exact response and artifact contract.
