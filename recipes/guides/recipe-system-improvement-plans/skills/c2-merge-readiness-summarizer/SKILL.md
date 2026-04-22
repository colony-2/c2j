# c2-merge-readiness-summarizer@v1

## Purpose

Create a merge decision package for the human checkpoint.

## Required inputs (`/src/inbox`)

- `implementation/summary.md`
- `validation/latest-status.json`
- `validation/output-tail.txt`
- git metadata/context artifacts

## Required outputs (`/src/outbox/merge`)

- `readiness-summary.md`
- `risk-checklist.json`

## `risk-checklist.json` shape

- `compatibility_risk` (`low|medium|high`)
- `operational_risk` (`low|medium|high`)
- `open_items` (list)
- `recommendation` (`merge|revise|cancel`)

## Execution steps

1. Summarize code/test outcomes in business terms.
2. Surface unresolved risks and blockers explicitly.
3. Provide deterministic recommendation for checkpoint review.

## Guardrails

- Merge is not optional at checkpoint: recommend `merge`, `revise`, or `cancel`.
- Keep rationale artifact-backed and auditable.

