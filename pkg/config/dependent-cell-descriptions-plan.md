# Dependent cell descriptions and `c2j cell add`

Status: implemented. This document records the behavior and verification scope.

Allow a project to describe its dependent cells in config and provide a command
that edits that config. Support a simple repository-string form and a detailed
mapping form as equal choices.
Descriptions appear in both human-readable and JSON cell listings.

Examples use `.c2/config.yaml`, the path used by the current checkout. The
implementation must reuse config discovery and the shared directory convention
from the ongoing `.c2j` to `.c2` rename, rather than introduce separate migration
or fallback behavior.

## Config format

Extend `dependents` sequence entries to accept either a repository string or a
mapping with required `repo` and optional `description`:

```yaml
pattern: 'github.com/acme/${{ cell }}'

dependents:
  - repo: github.com/acme/auth
    description: Authentication and authorization service
  - repo: github.com/acme/billing
    description: |
      Billing and subscriptions.
      Owns invoice generation and payment-provider integration.
  - github.com/acme/shared
```

Rules:

- Strings and mappings may be mixed in one list.
- `repo` retains today's repository-location requirement. Short names in YAML
  dependent entries remain unsupported; the CLI can expand them before writing.
- `description` is an optional literal string. Preserve internal newlines; do
  not evaluate templates or commands. Omitted or empty means no description.
- Require string values for both fields in the new mapping form. Reject missing
  or empty `repo`, unknown mapping fields, duplicate mapping keys, and nested
  values with errors identifying the entry and field.
- Validate repository strings in the simple form using the same repository rules
  as `repo` in the detailed form.
- An explicit list remains the complete allowed set: it overrides discovery and
  bypasses filtering, including the filter inferred from `pattern`.
- `dependents: []` still explicitly means no dependent cells.
- The existing `{command, filter}` object form is unchanged. Its command still
  emits repository locations, one per line; discovered entries have no description.
- Descriptions are display metadata. They do not affect authorization, repository
  identity, short-name derivation, refs, or job execution.

Keep existing exact-string deduplication semantics for resolution. For repeated
identical repository strings, retain the first position, use the nonempty
description if only one is supplied, and reject conflicting nonempty descriptions.
Do not introduce broader repository canonicalization into list resolution in this
change.

## Command interface

Add a singular `cell` command group while retaining the existing `c2j cells`
listing command:

```bash
c2j cell add github.com/acme/auth \
  --description 'Authentication and authorization service'

# Short names resolve through the existing project cell-name resolver.
c2j cell add billing --description 'Billing and subscriptions'

# Repeating add updates the existing entry's description.
c2j cell add auth --description 'Identity, sessions, and permissions'

# Explicitly clear a description.
c2j cell add auth --description ''

# Preview the resulting YAML without writing.
c2j cell add auth --description 'Identity service' --stdout

c2j cells
c2j cells --json
```

`c2j cell add <repo-or-short-name>` takes exactly one positional argument. It is
an idempotent add-or-update operation:

- Locate the nearest existing project config using the same discovery rules as
  other commands, including when invoked from a project subdirectory. Report the
  edited path in normal output.
- If no config file exists, fail with an actionable instruction to run
  `c2j init --no-skills` at the project root. Do not write into an inferred or
  arbitrary directory.
- Expand CLI short names through the existing resolver and store the resulting
  repository location. Full repository input should not require resolving an
  unrelated pattern or running config commands.
- Append a new entry at the end. Write a scalar if no nonempty description was
  supplied, or a mapping when it was.
- Match existing entries using the existing repository-equivalence helper. Retain
  the existing repository spelling and list position when updating.
- If more than one existing entry matches, fail with the matching entry locations
  so the user can remove ambiguity; do not silently merge hand-written entries.
- Omitting `--description` preserves an existing description. Use Cobra's flag
  presence information to distinguish omission from `--description ''`.
- Upgrade a matching scalar to a mapping when adding a description, carrying its
  comments over. Clearing a mapping's description removes that field but does not
  require converting the entry back to a scalar.
- If the requested result already exists, succeed without rewriting the file.
- `--stdout` emits only the complete proposed config YAML and performs the same
  resolution and validation as a write. Status messages must not pollute YAML.
- Normal output reports `added`, `updated`, or `unchanged`, the repo, and the path.
  No confirmation prompt is required.

For example, adding a description to this config:

```yaml
dependents:
  - github.com/acme/auth # Used by the API
```

produces the equivalent of:

```yaml
dependents:
  - repo: github.com/acme/auth # Used by the API
    description: Authentication and authorization service
```

## Automatic discovery

Use detected cells directly when automatic discovery is available. Keep discovery
live; do not copy its results into the config. There is no materialization command
or flag in this plan, and no new schema for combining discovery with manual entries.

- With an existing explicit list, edit that list directly.
- With no `dependents` key and no discovery source, create a manual list.
- With Go discovery or `dependents.command`, `c2j cells` continues to resolve the
  current allowed set each time. Preserve commands and filters in the file.
- For `cell add` in discovery mode, a cell already in the allowed detected set
  needs no manual entry. If no nonempty description was requested, succeed without
  editing and report that the cell is already detected.
- Adding an undetected cell or attaching a description in discovery mode is outside
  this first version. Return an actionable error explaining that `cell add` edits
  manual lists and the current project uses discovery. Do not silently replace
  discovery with a list or write metadata that will not be read.
- A discovery failure is an error, not evidence that the project has no cells.
  Leave the config untouched.
- If `dependents` is an object without an effective discovery source, preserve
  it and report that manual entries require the sequence form. Do not discard
  its filter settings as a side effect of adding a cell.

Structured descriptions apply to manually listed cells in this scope. Detected
cells remain usable without descriptions. Description overlays on live discovery
can be designed separately if needed.

## Listing behavior

Extend `c2j cells` with a `DESCRIPTION` column. Render multiline descriptions as
one display line and escape control characters so they cannot corrupt the table
or terminal. Render an absent description as `-`.

Add an optional `description` field to each `c2j cells --json` row, omitted when
empty. Preserve the existing JSON array structure, `short_name`, `repo`, and row
ordering. JSON retains description newlines and full text.

```json
[
  {
    "short_name": "auth",
    "repo": "github.com/acme/auth",
    "description": "Authentication and authorization service"
  }
]
```

## Implementation steps

1. Extend `pkg/config/config.go` with a dependency entry type carrying `Repo` and
   `Description`, and decoding for mixed scalar/mapping sequences. Add structured
   accessors such as `DependentEntries` and `AllowedDependentEntries`. Keep
   existing repo-only methods and compatibility aliases as projections, preserving
   callers and current filter/permission behavior. Account for the exported
   `DependentsConfig.Repos` field rather than silently breaking its users.
2. Add a focused config editor in `pkg/config`. Parse the source as `yaml.Node`
   and modify only the dependent subtree, preserving unrelated keys, key order,
   comments, scalar styles, and values. Avoid serializing through `fileConfig`,
   which would discard unknown fields and comments. YAML indentation may be
   normalized by the encoder; byte-for-byte formatting preservation is not promised.
   Reject multi-document input and ambiguous alias/merge-based dependent structures
   before editing, with an actionable error rather than rewriting shared nodes.
3. Validate the proposed bytes with the same config parser before writing, without
   running unrelated config commands. Write a temporary file in the same directory,
   preserve existing file permissions, and atomically rename it over the target.
   Clean up temporary files on failure. Reject symlink targets explicitly in this
   first version. Recheck that the source has not changed before replacement and
   fail on detected concurrent edits; this check is not a promise of cross-process
   locking. Keep unchanged files byte-identical.
4. Add a small command service under `cmd/c2j/internal/celledit` and Cobra wiring
   in `cmd/c2j/internal/cmd/cell.go` and `root.go`. Keep discovery, mutation, and
   output handling independently testable. Expose `--description` and `--stdout`
   with the semantics above.
5. Update `cmd/c2j/internal/configinspect/service.go` to consume structured entries
   and display descriptions. Existing repo-only consumers need no metadata changes.
6. Update `pkg/config/init_template.go`, README configuration/CLI examples, and
   bundled `skills/c2j-operations` CLI/runtime references. Show both supported
   list entry forms as simple and detailed alternatives, add/update examples,
   preview behavior, and direct use of automatically detected cells.

## Verification and completion criteria

Add focused tests covering behavior rather than just struct layout:

- Simple string lists and detailed mapping lists represent the same manual cell
  set; command/filter discovery remains live.
- Mixed lists, multiline descriptions, empty lists, malformed entries, duplicate
  descriptions, and repo-only accessor compatibility behave as specified.
- Listing preserves metadata through filtering and produces the documented table
  and JSON output, including descriptions containing newlines/control characters.
- CLI add works from a nested directory, appends once, updates in place, expands
  short names, preserves omitted descriptions, and clears explicitly empty ones.
- Equivalent repo spellings do not produce accidental duplicates; ambiguous
  existing matches produce an error without changes.
- Comments, unrelated fields, ordering, and permissions survive edits. A no-op
  retains exact file bytes. Invalid YAML, unsupported alias/merge structures,
  missing config, symlinks, detected concurrent edits, and write failures leave
  the original intact.
- Detected cells are used directly without copying them into config. Adding an
  already detected cell without a description leaves the file unchanged. Attempts
  to edit unsupported discovery metadata, add undetected cells in discovery mode,
  or proceed after discovery errors leave the file unchanged and explain why.
  An explicit list does not trigger discovery.
- `--stdout` returns valid proposed YAML while leaving files untouched.

Run the affected Go package tests for `pkg/config`, `configinspect`, `celledit`,
and `cmd`, plus init-template tests wherever they live. Perform a temporary-project
CLI smoke test: start with a commented list, add a described cell, update it,
repeat the same command, and inspect `cells --json` and the file diff. Run the
repository's required checks before merging implementation.

Done means both supported YAML forms work through the CLI, automatically detected
cells remain available directly, and descriptions survive a manual config
edit/load/list round trip. Removal,
renaming, per-cell refs, description overlays on live discovery, and structured
command output are outside this change.
