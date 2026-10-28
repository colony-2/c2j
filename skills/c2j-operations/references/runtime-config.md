<!-- Sources: README.md; pkg/config/init_template.go; cmd/c2j/internal/initconfig/service.go; cmd/c2j/internal/defaults; cmd/c2j/internal/configinspect. -->

# Runtime Config

## Current Cell

`submit` targets the current cell by default. Cell resolution usually comes from `.c2/config.yaml`; supported project types can also be auto-detected.

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

## Manual Dependent Cells

`dependents` supports simple repository strings and detailed entries with an
optional literal description. Both forms can appear in the same list:

```yaml
dependents:
  - github.com/acme/shared
  - repo: github.com/acme/auth
    description: Authentication service
```

Use full repository locations in YAML. `c2j cell add auth --description 'Identity service'`
can expand a short name through the project pattern before writing.
Add `--stdout` to preview; omit `--description` to preserve it or pass an empty
string to clear it. The editor preserves comments and unrelated fields.

A manual list is the complete allowed set and bypasses discovery filters.
Otherwise, Go or command-based discovery stays live and detected cells are used
directly, without copying them into config. Descriptions currently apply to manual
entries only. `c2j cells` displays descriptions; `--json` includes a `description`
field only when nonempty and preserves multiline text.

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

Skill install metadata is recorded in `.c2/skills-lock.yaml`. Lock paths are relative
to the project root for project scope and to `CODEX_HOME` (default `~/.codex`)
for user scope; the lock does not store machine-specific absolute install paths.

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

Declare repository dependencies in the existing `.c2/config.yaml`:

```yaml
packages:
  - nix:nixpkgs#go
  - nix:nixpkgs#openssl
  - pnpm:typescript@5.8.3
  - uv:ruff==0.11.2
```

These use the same references as recipe `execution.packages`, node
`execution_needs.packages`, and op `dependencies`. Pin versions and Nix revisions
for repeatable resolution. c2j reads the active workspace's restored config
before each live task, installs through the shared cache, and exposes executables
and Nix native-library build variables to the operation and its subprocesses.
Recipe/node/op declarations take precedence for executable selection. Cached
replay and fully mocked recipe tests skip discovery and installation.

Nix supports libraries with no executables, including development headers,
compiler wrappers, and pkg-config discovery. pnpm/uv declarations supply isolated
CLI applications, not project import environments. Repository specs must be
portable registry or remote references; local paths and symlinked configs fail.
See [Execution tools](https://github.com/colony-2/c2j/blob/main/EXECUTION_TOOLS.md)
for scope rules, native-library support, and cache/provider requirements.

Commands and extensions run directly in the worker environment. c2j does not
read `.shai/config.yaml` or create a container per operation. Provision required
images, package managers, mounts, environment variables, and isolation externally before
starting the worker. Remove former generic `sandbox` inputs and forwarding
parameters when migrating recipes; see [breaking release notes](https://github.com/colony-2/c2j/blob/main/RELEASE-NOTES-Remove-Op-Sandboxing.md).

Stored recipes and history are not rewritten. Finish jobs requiring the old
runtime with a pinned older release, or resubmit deliberately migrated recipes.
