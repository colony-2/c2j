---
name: c2ops-extension-ops
description: Choose and call trusted selector-backed c2ops extension ops from `colony-2/c2ops`. Use for c2j recipes that rely on c2ops selectors such as llm, llm2, codex, codex/run_skill, rule_gate, gha, gha-many, pydantic, aider, or litellm, and for writing richer `extension_execution` invocation snippets with c2j template, artifact, and sandbox conventions.
---

# c2ops Extension Ops

Use this aggregate skill for selector-backed ops from `colony-2/c2ops`.

## Workflow

1. Read [references/trusted-c2ops.md](references/trusted-c2ops.md) to confirm the selector comes from the trusted c2ops source and to pick the stable selector form.
2. Read [references/selector-patterns.md](references/selector-patterns.md) for invocation snippets, input-shaping patterns, artifacts, sandbox use, and examples for common c2ops selectors.
3. When authoring a full recipe, use `$c2j-recipes` alongside this skill so the surrounding recipe syntax, templates, artifacts, testing, and git-state rules stay correct.

Never infer trust from an arbitrary `op` selector. Use selectors from the allowlist or ask the user to approve a new trusted source explicitly.
