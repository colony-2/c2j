# Organized Config Shape, Pattern Defaults, and Commented `c2j init`

## Problem

The current config shape works, but it is harder to read and teach than it needs to be.

- `parent` is really a base/default sentinel, not a parent config file.
- `cells`, `root_cell`, `canonical_repo`, and `default_ref` are flat and repo-centric.
- There is no built-in `c2j init` flow that writes a commented starter config.
- Short cell names are not first-class, so users have to work directly with repository strings.

The result is a config that is valid for the machine but not especially organized for the human editing it.

## Goals

- Reorganize the config by intent.
- Make `base: go` the one-line happy path for the common case.
- Support short cell names through a single repo pattern.
- Keep `c2j init` output commented and easy to edit in place.
- Preserve strict validation where defaults or expansion are ambiguous.

## Proposed Config Shape

```yaml
base: go

dependents:
  command: <command>
  filter: <regex>

self:
  repo: <repo | short-name | { command: <command> }>
  ref: <ref | { command: <command> }>

root:
  repo: <repo | short-name | { command: <command> }>
  ref: <ref | { command: <command> }>

pattern: <pattern>
```

Recommended Go shape:

```go
type fileConfig struct {
    Base       string           `yaml:"base"`
    Dependents DependentsConfig `yaml:"dependents"`
    Self       CellRefConfig    `yaml:"self"`
    Root       CellRefConfig    `yaml:"root"`
    Pattern    string           `yaml:"pattern"`
}

type DependentsConfig struct {
    Command string `yaml:"command"`
    Filter  string `yaml:"filter"`
}

type StringOrCommand struct {
    Value   string
    Command string
}

type CellRefConfig struct {
    Repo StringOrCommand `yaml:"repo"`
    Ref  StringOrCommand `yaml:"ref"`
}
```

Implementation note:

- `repo` and `ref` should accept either a scalar string or an object with `command`
- a plain scalar string is treated as the literal value
- this requires a small custom unmarshal helper, but keeps the YAML compact in the common case

Recommended field mapping from today:

- `parent` -> `base`
- `cells.command` -> `dependents.command`
- `cell_filter` -> `dependents.filter`
- `canonical_repo` -> `self.repo`
- `default_ref` -> `self.ref`
- `root_cell` -> `root.repo`

`c2j init` should write only the new shape. No migration behavior is needed because there are no existing configs to preserve.

## `pattern` Syntax

I recommend that `pattern` use the existing `${{ ... }}` delimiter style, but in a very constrained way:

```yaml
pattern: 'https://foo.bar/baz/boo-${{ cell }}.git'
```

Rules:

- `pattern` must contain exactly one `${{ cell }}` placeholder.
- No other expressions are allowed.
- This is string substitution only. It is not general CEL evaluation.

Why this instead of `\1`:

- `\1` reads like regex replacement, but the primary operation here is expansion, not regex replacement.
- `${{ cell }}` already looks native to c2j users.
- It is much easier to explain in a commented config template.

Example:

- pattern: `https://foo.bar/baz/boo-${{ cell }}.git`
- short name: `monkey`
- expanded repo: `https://foo.bar/baz/boo-monkey.git`

## Scalar Source Encoding

This proposal uses string-or-object source encoding for `repo` and `ref`.

Recommended form:

```yaml
self:
  repo: cheetah
  ref: main

root:
  repo:
    command: git remote get-url origin
  ref:
    command: git symbolic-ref --short HEAD
```

Rules:

- plain string means the literal value
- object form means an explicit command source
- object form supports `command` only

Why this over `$(...)`:

- no shell mini-language inside YAML strings
- no need for sibling names like `repo_command`
- consistent shape across `repo` and `ref`
- easier validation and clearer error messages

This proposal does not support `$(...)` syntax.

## Repo vs Short Name Classification

Short names are restricted to:

```text
^[A-Za-z0-9._-]+$
```

Classification rules:

- if the value contains `:` or `/`, it is a repo
- otherwise, if it matches `^[A-Za-z0-9._-]+$`, it is a short name
- otherwise, it is invalid

This makes short-name detection explicit and keeps it independent from transport-specific parsing.

## Repo Value Semantics

`self.repo` and `root.repo` should accept three forms:

- repo
  - example: `github.com/acme/boo-cheetah`
  - example: `https://foo.bar/baz/boo-cheetah.git`
  - example: `file:///src/boo-cheetah`
- short name
  - example: `cheetah`
- command source
  - example:
    ```yaml
    repo:
      command: git remote get-url origin
    ```

Recommended behavior:

- if the resolved value is a repo, use it as-is
- otherwise, if `pattern` is configured, treat it as a short name and expand through `pattern`
- otherwise, return a validation error

`dependents.command` remains an explicit command field. It must emit a bare list of repos, one per line.

## Ref Value Semantics

`self.ref` and `root.ref` should accept:

- literal refs
  - example: `main`
- command sources
  - example:
    ```yaml
    ref:
      command: git symbolic-ref --short HEAD
    ```

Recommended behavior:

- `self.ref` defaults to `main`
- `root.ref` defaults to `self.ref`

## Defaulting Rules

### Generic defaults

- `self.ref` defaults to `main`
- `root.ref` defaults to `self.ref`
- `root.repo` defaults to expanding `root` through `pattern` when `pattern` is known
- `dependents.filter` defaults to a regex derived from `pattern`
- if `pattern` is absent, short-name expansion is disabled
- if `pattern` is absent, `root.repo` has no default; it must be set explicitly to submit a root ticket

### `base: go`

`base: go` replaces today’s `parent: go`, but it should do more than the current implementation.

Recommended behavior:

1. Read the main module path with `go list -m -f '{{.Path}}'`.
2. Normalize that module path into the repo name we use for config values by removing any trailing Go major version segment such as `/v2`.
3. Set `self.repo` from that derived repo name if `self.repo` is not explicitly configured.
4. Set `self.ref` to `main` if it is not explicitly configured.
5. Read dependent module paths with `go list -m -f '{{if not .Main}}{{.Path}}{{end}}' all`.
6. Normalize each dependent the same way by removing any trailing Go major version segment such as `/v2`.
7. If `dependents.command` is not explicitly configured, use that normalized dependent repo list as the default command output shape: one repo per line.
8. Try to derive `pattern` from `self.repo` using the first-dash rule on the last path segment.
9. If `pattern` is derived, default `root.repo` to `root`, default `root.ref` to `self.ref`, and default `dependents.filter` from the pattern.

This keeps `base: go` explicit about the two things it learns from Go tooling:

- the current repo name
- the dependent repo names

No direct `go.mod` parsing is required for those lookups.

This gives the intended happy path:

```yaml
base: go
```

For a module whose repo name resolves to:

```text
github.com/acme/boo-cheetah
```

the derived values would be:

- `self.repo` -> `github.com/acme/boo-cheetah`
- `self.ref` -> `main`
- `pattern` -> `github.com/acme/boo-${{ cell }}`
- `root.repo` -> `github.com/acme/boo-root`
- `root.ref` -> `main`
- `self` short name -> `cheetah`
- `dependents.command` -> Go module discovery normalized to repo names, one per line
- `dependents.filter` -> derived regex for the same pattern family

## Go Pattern Derivation Rule

The requested derivation rule is:

- inspect the last segment of the resolved self repo
- find the first `-` in that last segment
- if a dash exists, keep the prefix including the dash as the stable family prefix
- if no dash exists, use the trailing `/` as the family boundary instead
- treat the remainder after that boundary as the current cell name

Example:

- `boo-cheetah` -> prefix `boo-`, cell `cheetah`
- derived pattern -> `boo-${{ cell }}`
- `foo/bar` -> boundary `foo/`, cell `bar`
- derived pattern -> `foo/${{ cell }}`

## Derived Filter Behavior

When `dependents.filter` is omitted and `pattern` is set, derive the filter automatically from the pattern.

For:

```yaml
pattern: 'github.com/acme/boo-${{ cell }}'
```

derive something equivalent to:

```text
^github\.com/acme/boo-([A-Za-z0-9._-]+)$
```

That gives us two useful properties from one definition:

- short name -> repo expansion
- repo -> short name matching for allowed dependents and CLI targeting

This is better than asking users to manually keep `pattern` and `dependents.filter` in sync.

## `c2j init` Behavior

`c2j init` should now do two jobs:

1. write a commented config template
2. prefill or simplify it when `base: go` inference succeeds

Recommended behavior:

- if discovery succeeds cleanly, write a minimal valid config with comments around the inferred defaults
- if discovery is partial, write the commented template plus inline notes about what still needs editing
- fail if `.c2j/config.yaml` already exists unless `--force` is passed
- support `--stdout` for previewing the generated file

## Example Generated File

For the simple inferred case:

```yaml
# c2j project config
#
# Derived from go.mod. In this repo, `base: go` is enough to infer:
# - self.repo
# - self.ref
# - pattern
# - root.repo = root
# - root.ref = self.ref
# - dependents.command
# - dependents.filter
base: go

# Optional override for short-name expansion.
# pattern: 'github.com/acme/boo-${{ cell }}'

# Optional overrides.
# self:
#   repo: cheetah
#   ref: main
#
# root:
#   repo: root
#   ref: main
#
# dependents:
#   filter: '^github\\.com/acme/boo-([A-Za-z0-9._-]+)$'
```

For the explicit case:

```yaml
# c2j project config
base: go

pattern: 'https://foo.bar/baz/boo-${{ cell }}.git'

dependents:
  command: |
    printf '%s\n' \
      https://foo.bar/baz/boo-root.git \
      https://foo.bar/baz/boo-monkey.git \
      https://foo.bar/baz/other.git

self:
  repo: cheetah
  ref: main

root:
  repo: root
  ref: main
```

In that example:

- `self.repo: cheetah` resolves to `https://foo.bar/baz/boo-cheetah.git`
- `root.repo: root` resolves to `https://foo.bar/baz/boo-root.git`
- `root.ref: main` stays explicit, but could also be omitted and inherit `self.ref`
- `dependents.filter` defaults to the pattern-derived regex, so `other.git` is excluded

## Runtime Changes

The config package should resolve three related concepts explicitly:

- short name
- repo value
- allowed dependent repo set

Suggested helpers:

```go
type CellPattern interface {
    Expand(name string) (string, error)
    Match(repo string) (name string, ok bool)
    Regex() string
}

func (c *ProjectConfig) SelfRepo(ctx context.Context) (string, error)
func (c *ProjectConfig) SelfRef(ctx context.Context) (string, error)
func (c *ProjectConfig) RootRepo(ctx context.Context) (string, error)
func (c *ProjectConfig) RootRef(ctx context.Context) (string, error)
func (c *ProjectConfig) DependentRepos(ctx context.Context) ([]string, error)
func (c *ProjectConfig) AllowedDependentRepos(ctx context.Context) ([]string, error)
func (c *ProjectConfig) ExpandCellName(ctx context.Context, value string) (string, error)
func (c *ProjectConfig) CellNameFromRepo(ctx context.Context, repo string) (string, bool)
```

`cmd/c2j/internal/submitjob/service.go` should then:

- accept `--cell monkey`
- expand it through `pattern` to a repo value
- normalize that repo value through `compiler.NormalizeGitRepositorySource(...)` before Git operations
- use the short name in workflow context
- use the normalized repository source for Git operations

## Validation Rules

- `base` currently supports only `go`
- `pattern` must contain exactly one `${{ cell }}`
- short names must match `^[A-Za-z0-9._-]+$`
- any `repo` value containing `:` or `/` is treated as a repo, not a short name
- `repo` and `ref` command sources must resolve to a single trimmed line
- if `pattern` is absent, short names are invalid in config and CLI input
- if `pattern` is absent, `root.repo` must be explicit before submitting a root ticket
- `dependents.command` must emit repos, one per line
- if `dependents.filter` is supplied explicitly, it must compile as a regex
- if `base: go` cannot derive a pattern automatically, that is not itself an error
  - it only becomes an error when a feature requires short-name expansion but no explicit pattern exists

## Feedback

I think the direction is good.

- Renaming `parent` to `base` is clearer.
- Grouping `dependents`, `self`, and `root` makes the file easier to scan.
- A single `pattern` is much lighter than explicit bidirectional mapping tables for the common dash-family repo layout.
- `base: go` as the one-line configuration is the right UX target.

The main caveats are:

- The first-dash heuristic is opinionated. `foo-bar-baz` becomes prefix `foo-` and cell `bar-baz`. If that is the intended convention, great. If not, this will surprise people.
- `repo` and `ref` as string-or-object values are a little more work to parse, but I think that tradeoff is worth it to avoid `$(...)` and avoid `repo_command`-style sibling fields.
- Repo values may be canonical repo names, URLs, or file paths. That flexibility is useful, but it means normalization has to stay centralized.
- Making `root.ref` explicit is better than relying on an unstated inheritance rule, even if the default remains `self.ref`.

## Alternative Shapes Considered for Command Sources

### 1. `$(...)` inside strings

Example:

```yaml
self:
  repo: $(git remote get-url origin)
```

Pros:

- compact
- shell users understand it immediately

Cons:

- embeds a mini command language in a scalar string
- quoting and escaping are less obvious
- validation errors are more indirect

### 2. Sibling keys like `repo` / `repo_command`

Example:

```yaml
self:
  repo_command: git remote get-url origin
```

Pros:

- simple parser

Cons:

- field explosion
- awkward naming
- least organized shape

### 3. String-or-object source encoding

Example:

```yaml
self:
  repo:
    command: git remote get-url origin
```

Pros:

- single logical field name
- explicit source type
- scales cleanly to `ref`

Cons:

- requires custom YAML unmarshal

This is the option this proposal adopts.

## Remaining Open Point

Most of the format and resolution behavior is now explicit.

The main behavioral question I still see is the first-dash heuristic itself:

- `foo-bar-baz` becomes prefix `foo-` and cell `bar-baz`

If that convention is intentional, the proposal is coherent.
If the intended split is sometimes `foo-bar-` plus `baz`, then we need a different derivation rule before implementation.

## Recommendation

Update the proposal in this direction:

- adopt the organized config shape
- make `${{ cell }}` the only supported pattern placeholder
- add `root.ref` with default inheritance from `self.ref`
- use string-or-object source encoding for `repo` and `ref`; do not support `$(...)`
- let `base: go` derive `self.repo`, `self.ref`, `pattern`, `root.repo`, `root.ref`, and dependents defaults when possible
- keep `c2j init` commented, but allow it to emit a very small file when inference is confident

The one thing I would keep strict is this:

- never silently guess a pattern when the first-dash derivation does not apply cleanly

That preserves the simple happy path without hiding ambiguity.
