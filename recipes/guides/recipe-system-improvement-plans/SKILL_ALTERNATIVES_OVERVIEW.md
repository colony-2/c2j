# Skill-Oriented Alternatives (Checkpoint-Preserving)

## Context

Current recipes expose inner implementation loops as many explicit states. That keeps control visible, but creates repetitive orchestration YAML.

If Codex Skills become first-class in `codex.exec`, we can keep major checkpoints while moving detailed loops into skill-guided execution.

## Alternatives

1. `DEVELOPMENT_PATTERN_SKILLS.md`
   - Concrete skill catalog mapped to lifecycle stages and artifact/status contracts.
   - Executable templates: `skills/*/SKILL.md`.
2. `DEVELOPMENT_PATTERN_SKILL_PACKS.md`
   - Pack-level composition and checkpoint-centric recipe usage pattern.
3. `4A-skill-aware-codex-exec.md`
   - Minimal engine enhancement: make `codex.exec` skill-aware with structured progress/event artifacts.
   - Recipe stays phase-oriented; inner loops move into skill behavior.
4. `4B-checkpoint-centric-recipe-refactor.md`
   - Refactor `new-ticket` to two explicit human checkpoints and one skill-supervised implementation state.
   - Retains backtrack and cancel behavior.
5. `4C-skill-packaging-and-governance.md`
   - Defines how skill packs are versioned, tested, approved, and rolled out per project/cell.
6. `4D-stepwise-codex-checkpointing-requirements.md`
   - Defines the non-long-lived process model: one Codex process per step with `sessionId`-based resume.
7. `4E-codex-exec-user-api-and-multi-skill-examples.md`
   - Specifies concrete user API fields and sample multi-skill stepwise flow.
8. `4F-multi-git-ref-skill-sources-for-codex-exec.md`
   - Adds a multi-git-ref source pattern so one `codex.exec` call can mount platform and team skill repos together.

## Recommended sequence

1. Implement 4A first (no large recipe rewrites yet).
2. Add 4F so recipes can consume versioned platform skill repos without blob wiring.
3. Pilot 4B on `new-ticket` once 4A and 4F are stable.
4. Add 4C governance before broad migration.

## Decision questions

1. Should skills be pinned per recipe version (strict reproducibility) or centrally managed by project defaults (operational flexibility)?
2. Do you want the implementation state to run until convergence automatically, or stop at each internal milestone for optional review?
3. Should contrarian validation remain as explicit recipe ops, or be run by a dedicated skill inside implementation with artifact outputs only?
