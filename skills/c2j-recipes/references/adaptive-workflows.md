# Shared build decisions

Identify the job's captured recipe source/revision before applying workflow policy. The public `build.yaml` uses adaptive2; explicit older overrides and creation have their own rules. A moving main link is discovery, not proof of a captured job's behavior. The older adaptive README's requester-written handoff shortcuts are not adaptive2 defaults. Shared helpers under `recipes/adaptive/` remain used; do not discard that directory wholesale.

Recipes owns policy. Reviewed on 2026-11-11 at `2fa3296836b2faf8aa1ff8ece7bb44ac1b15bda4`:

- [Build process](https://github.com/colony-2/recipes/blob/2fa3296836b2faf8aa1ff8ece7bb44ac1b15bda4/ADAPTIVE_BUILD_PROCESS.md)
- [Adaptive2 decisions and recovery](https://github.com/colony-2/recipes/blob/2fa3296836b2faf8aa1ff8ece7bb44ac1b15bda4/recipes/adaptive2/README.md)
- [Handoff documents](https://github.com/colony-2/recipes/blob/2fa3296836b2faf8aa1ff8ece7bb44ac1b15bda4/guides/adaptive2/HANDOFF_DOCUMENTS.md)
- [Inspection playbook](https://github.com/colony-2/recipes/blob/2fa3296836b2faf8aa1ff8ece7bb44ac1b15bda4/guides/JOB_INSPECTION_PLAYBOOK.md)
- [Shared helpers and older workflow](https://github.com/colony-2/recipes/blob/2fa3296836b2faf8aa1ff8ece7bb44ac1b15bda4/recipes/adaptive/README.md)

Use those sources for complete workflow rules; this reference describes consequential choices, not a replacement workflow. For exact persisted reads and exports, use [inspection and recovery](../../c2j-operations/references/inspection-and-recovery.md).

## Assignment and approval

The receiving owner authors `handoff.md` with scope and acceptance. Keep that assignment unchanged when selecting an execution handoff. Supporting evidence may be added, updated or reselected without renewed agreement, including unchanged inbox files selected directly. Evidence must not silently contradict or expand the assignment. Return changed scope to its owner for a revised handoff. Latest human direction supersedes earlier requirements; update affected assignments before presenting them again. A consultation agreement or supplied handoff is not native implementation approval.

For a fresh replacement of an unsuccessful execution, `replaces` names that failed/cancelled attempt. `resume_from` names the previous completed/cancelled job carrying approved direction; it can be a different job or come from another parent. Inspect its latest status, exact native human decision and receiver assignment. Preserve original request, design, tests, latest feedback and selected recovery provenance in the handoff. The receiving workflow verifies approval and current same-cell scope before implementation. Fresh jobs do not automatically inherit old sessions or history.

Distinguish these outcomes:

| Finding | Next action |
| --- | --- |
| Native approval found and current assignment matches | Recover that direction and retest selected work; avoid repeating consultation just to recreate records. |
| Approval absent or scope changed | Use ordinary direction review; artifacts alone cannot authorize implementation. |
| Approval inspection denied, expired or otherwise unreadable | Preserve the diagnostic; adaptive2 stops with `ADAPTIVE_RECOVERY_INSPECTION_FAILED`, rather than pretending approval is absent and silently opening another review. |
| Recovery source still active | Wait or resolve its outcome; do not start duplicate implementation. |

Runtime restart selects a recorded boundary and creates a continuation; it is not interchangeable with replacement build/approval recovery.

## Follow results and stop requests

Track exact child jobs, attempts and replacement lineage. Reuse successful work, wait for active work, and address failed/cancelled outcomes. An open goal does not turn a failed attempt into success. A submitted identity proves submission only. For child startup/collection errors, inspect the child's own `job show` and earliest failed tasks; distinguish the child's error from a parent's inspection error. Retain actual direct-inspection denials. Do not silently replace deliberately stopped work.

A human stop halts broader work and publication, retains evidence and leaves unfinished work unfinished. It does not implicitly cancel separately running children; inspect their status and use authorized cancellation separately when needed. Do not mark the goal achieved to end a stopped or exhausted attempt. Automatic limits are not successful completion or permission to broaden scope.

## Missing tools and resources

Inspect `c2j self`, `.c2/config.yaml`, installed help and current guides before declaring behavior unsupported. Use compatible installed tools first. In adaptive2, a missing worker executable can be returned with `next: blocked` and `packages`, for example a resolvable pinned `uv:ruff==0.11.2` with bounded argv probe `["ruff", "--version"]`. Choose a version for the actual requirement, not because this example names it. Go probes use `["go", "version"]`.

Keep exact reference/probe pairs and setup errors. Normal-cell accepted package policy can be reused; otherwise the recipe obtains the specific decision and validates in a separate child before adoption. Do not edit active worker package configuration to approve your own candidate. Use project dependency managers for application libraries. Missing memory, CPU, storage, SDK/base or Docker needs a concrete executor capability/resource request with the observed failure, not a package promise. Provisioning is external; historical proposals do not prove dynamic allocation exists. Retain human exclusions and deferred checks; do not repeatedly request an excluded platform. An operational denial identifies missing capability, not automatically a human product decision.

Recipe definitions and tested workspace state are separate inputs. Incoming/current snapshot selection is documented in [testing and CLI](testing-and-cli.md#starting-from-workspace-state). If a Git fetch fails for an unpublished recipe, inspect supported local recipe inputs and validate the changed route with a bounded check. A published fixture cannot validate newly authored unpublished recipe content.

## Evidence and delivery

Select the actual files needed by the receiver or review, with linked detailed evidence. Record limitations per requirement; missing proof cannot become a pass. For visible UI changes, retrieve retained captures first, then capture and inspect actual relevant states and sizes; report fixture applicability. Screenshots are not required for nonvisual skill edits. Follow actual publication results before recommending another delivery: publication can fail after merge. Do not repeat a merge or delivery from a generic failure message without inspecting what succeeded.
