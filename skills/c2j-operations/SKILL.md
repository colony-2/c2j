---
name: c2j-operations
description: Operate existing c2j projects, cells, recipes, and jobs without doing full recipe authoring. Use for `c2j init`, `self`, `cells`, submitting a known recipe, continuing jobs, listing jobs or child jobs, checking ready counts, running one/any/loop workers, embedded JobDB runs, runtime config, inputs/artifacts flags, JSON output, and debugging c2j CLI or JobDB invocation behavior.
---

# c2j Operations

Use this skill when the user wants to operate c2j rather than design recipe YAML.

## Workflow

1. Read [references/cli-reference.md](references/cli-reference.md) for command syntax, flags, and common embedded/local workflows.
2. Read [references/job-workflows.md](references/job-workflows.md) for submit/run/list/ready/children flows, wait policies, input modes, and exit behavior.
3. Read [references/runtime-config.md](references/runtime-config.md) for `.c2j/config.yaml`, current-cell resolution, JobDB target resolution, `c2j init`, and embedded versus remote runtime behavior.

Prefer `--embed` for local smoke tests and `--json` when another tool or script needs structured output. When diagnosing a failed command, capture the exact command, working directory, config source, target cell, JobDB URI mode, and whether the job was already submitted.
