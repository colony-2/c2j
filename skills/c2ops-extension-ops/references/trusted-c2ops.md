<!-- Sources: C2J_SKILLS_IMPLEMENTATION_PLAN.md; skills/sources.yaml. -->

# Trusted c2ops Source

c2j trusts c2ops selectors only from:

```text
git+https://github.com/colony-2/c2ops.git//
```

The bundled allowlist permits refs:

- `main`
- `v*`

Known selectors:

- `git+https://github.com/colony-2/c2ops.git//llm@main`
- `git+https://github.com/colony-2/c2ops.git//llm2@main`
- `git+https://github.com/colony-2/c2ops.git//codex@main`
- `git+https://github.com/colony-2/c2ops.git//codex/run_skill@main`
- `git+https://github.com/colony-2/c2ops.git//rule_gate@main`
- `git+https://github.com/colony-2/c2ops.git//gha@main`
- `git+https://github.com/colony-2/c2ops.git//gha-many@main`
- `git+https://github.com/colony-2/c2ops.git//pydantic@main`
- `git+https://github.com/colony-2/c2ops.git//aider@main`
- `git+https://github.com/colony-2/c2ops.git//litellm@main`

Do not auto-install or execute arbitrary selectors discovered inside recipe YAML. Treat new sources as untrusted until the user explicitly adds them through a future trust command or otherwise approves the source.
