# c2ops Selectors

Read the bundled [c2ops catalog](../../c2ops-extension-ops/references/trusted-c2ops.md) for available packages, required inputs, and the audited upstream revision. Read [selector patterns](../../c2ops-extension-ops/references/selector-patterns.md) for rule gates, GitHub Actions, coding, LLMs, artifacts, and session objects.

Prefer published package selectors directly in `op:`:

```yaml
sequence:
  - id: run_codex
    op: nix:github:colony-2/c2ops/published#codex
    inputs:
      prompt: "{{ inputs.prompt }}"
```

`extension_execution` remains available: put the selector under `inputs.selector` and the extension's parameters under `inputs.inputs`. Bind artifacts at the recipe node level and pass paths through `context.environment.op.inbox`.

`published` tracks available builds; use a verified publication commit for reproducibility. Bare `codex`/`skill.run` names do not select c2ops. `skill.run` uses package attribute `skill-run`. Check the selected manifest rather than borrowing input/output fields from a similarly named op.

`rule_gate` requires `rules`, not `subject`; route on its `outputs.ok`. `pydantic` requires `default_provider` and `default_model`, not `data`. Codex resumes with the immutable `session` object, while Kimi and Aider use their separate `sessionId` contracts.
