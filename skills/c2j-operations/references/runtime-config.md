<!-- Sources: README.md; pkg/config/init_template.go; cmd/c2j/internal/initconfig/service.go; cmd/c2j/internal/defaults; cmd/c2j/internal/configinspect. -->

# Runtime Config

## Current Cell

`submit` targets the current cell by default. Cell resolution usually comes from `.c2j/config.yaml`; supported project types can also be auto-detected.

Inspect resolution:

```bash
c2j self
c2j self --json
```

List allowed dependent cells:

```bash
c2j cells
c2j cells --json
```

## Config Initialization

Generate a starter config:

```bash
c2j init
```

Preview without writing files or installing skills:

```bash
c2j init --stdout
```

Overwrite an existing config:

```bash
c2j init --force
```

Bundled skill install controls:

```bash
c2j init --no-skills
c2j init --skills-force
c2j init --skills-scope project
c2j init --skills-scope user
c2j init --with-op-skills
```

Skill scopes:

- `project`: `.agents/skills/<skill-name>`.
- `user`: `${CODEX_HOME:-$HOME/.codex}/skills/<skill-name>`.
- `auto`: project scope.

Skill install metadata is recorded in `.c2j/skills-lock.yaml`.

## JobDB Targets

Commands accept `--jobdb`. Use `--embed` where exposed to select `embed:///` for local runs.

Remote JobDB values normally include enough information to resolve tenant/runtime targeting. If a command says `--jobdb is required`, check the flag, environment, and project config resolution.

## Troubleshooting

When a command targets the wrong cell, run `c2j self --json` and verify `short_name`, `repo`, `ref`, `root_repo`, `root_ref`, and `pattern`.

When no jobs are visible, compare the target tenant/cell, stores/status filters, and whether the command is pointed at embedded or remote JobDB.

## Recipe Node Workspaces

`workspace: {cell: cellB}` on a recipe node changes its working data, while the submitted job stays in its original cell. `c2j list` and job provenance continue to use that owner. Inside commands, `C2J_WORKSPACE_CELL_NAME` identifies the active workspace and `C2J_WORKSPACE_SCOPE_ID` identifies an overridden scope; `C2J_CURRENT_*` remains job provenance.

Submission captures effective cell naming settings for node selectors. Workers do not consult their own current directory's config. Short names require that captured pattern; explicit repository locations also work. Upgrade recipe compilers and task workers together before submitting recipes with workspace declarations. Custom hosts registering workers individually must add `compiler.NewWorkspaceResolutionTaskWorker()` and use the updated JobDB schema.

## Operation Execution Environment

Commands and extensions run directly in the worker environment. c2j does not
read `.shai/config.yaml` or create a container per operation. Provision required
images, tools, mounts, environment variables, and isolation externally before
starting the worker. Remove former generic `sandbox` inputs and forwarding
parameters when migrating recipes; see [breaking release notes](../../../RELEASE-NOTES-Remove-Op-Sandboxing.md).

Stored recipes and history are not rewritten. Finish jobs requiring the old
runtime with a pinned older release, or resubmit deliberately migrated recipes.
