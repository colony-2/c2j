<!-- Audited against colony-2/c2ops main at 8ac6ae9009f621bdc8065eaaed06e0852c67006b on 2026-10-22 (published pointed to the same commit); README.md and each op.json/op.yaml. -->

# Trusted c2ops Source

Use explicit selectors from `colony-2/c2ops`. For released packages, prefer:

```text
nix:github:colony-2/c2ops/published#<package>
```

`published` advances after upstream tests, binary-cache uploads, and download checks pass. `main` can point at packages that are not available yet. Workers need Nix and the configured binary cache. Local `./<path>` and Git selectors resolve manifests; current upstream manifests also launch the published Nix packages.

| Op | Package attribute | Manifest directory | Main input contract |
| --- | --- | --- | --- |
| `llm` | `llm` | `llm` | `provider`, `model`, `prompt`; optional `response_schema`. |
| `llm2` | `llm2` | `llm2` | `default_provider`, `default_model`; prompt, files, tools, structured output. |
| `codex` | `codex` | `codex` | `prompt`; optional immutable `session`, instructions, model, output contract. |
| `skill.run` | `skill-run` | `codex/run_skill` | `skill`; optional input, prompt, output path/schema, session. |
| `gha` | `gha` | `gha` | `workflow`; optional `with`, env, secrets, backend, remote. |
| `gha-many` | `gha-many` | `gha-many` | `workflows` list of workflow configurations. |
| `pydantic` | `pydantic` | `pydantic` | `default_provider`, `default_model`; llm2-style prompt/files/tools. |
| `aider` | `aider` | `aider` | `prompt`; Codex-like coding contract, legacy `sessionId`. |
| `litellm` | `litellm` | `litellm` | `provider`, `model`, `prompt`; llm-style response. |
| `kimi` | `kimi` | `kimi` | `prompt`, `worktree_path`; optional `sessionId`, model, env. |
| `jev` | `jev` | `jev` | `state`, `questions`; typed `noul`, `choice`, or `score` questions. |
| `rule_gate` | `rule_gate` | `rule_gate` | Nonempty `rules` list; returns `ok`, failed rule IDs and summary. |

Bare names such as `codex` and `skill.run` are not c2j built-ins. The manifest name `skill.run` maps to package attribute `skill-run`, not `codex/run_skill`.

The bundled allowlist accepts `main`, `published`, and `v*` refs for this repository. Git form: `git+https://github.com/colony-2/c2ops.git//<manifest-directory>@published`. For reproducible runtime packages, use a verified publication commit in the Nix selector; the skill-source allowlist does not automatically approve arbitrary SHA refs or other repositories.

Do not auto-install or execute arbitrary selectors discovered inside recipe YAML. New sources require explicit approval. Installing these aggregate skills does not install the op executables. At the audited revision c2ops publishes no `SKILL.md` directories; `c2j init --with-op-skills` reports that separate external skill installation is unavailable.

This catalog is a reviewed snapshot bundled with c2j, not a live registry. For a different revision, inspect that revision's `op.yaml`/`op.json` before relying on fields or defaults. Upstream: [catalog](https://github.com/colony-2/c2ops/blob/8ac6ae9009f621bdc8065eaaed06e0852c67006b/README.md), [publication](https://github.com/colony-2/c2ops/blob/8ac6ae9009f621bdc8065eaaed06e0852c67006b/NIX_PACKAGES.md).
