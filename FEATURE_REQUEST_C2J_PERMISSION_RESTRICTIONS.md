# c2j: inherit permissions and override individual fields

Owner: c2j.

## Problem

An explicit `perms.jobdb` replaces the entire delegated grant. Setting only
`jobs.submit: false` also removes secret access needed for authenticated Git
checkout. This caused job `3Li7sIC4c53lVtcIbjkyCiHXIXd` to fail before Codex started
with `secret resolution unavailable`.

A complete grant works today, but requires a shared recipe to name the deployment's
Git secrets. Recipes should be able to restrict submission while retaining normal
repository access through the selected Git profile.

## Permission model

By default, `perms.jobdb` declarations replace the inherited delegated grant. A
recipe granting `{x, y}` may contain a node granting `{x, y, z}` or just `{z}`.
Only explicit `perms.limit.jobdb`, include-boundary authority, and the owning
lease impose ceilings. Removing a capability from an ordinary grant does not
forbid a descendant from requesting it again.

## Usage and semantics

Opt in to inheritance when changing individual fields:

```yaml
perms:
  inherit: true
  jobdb:
    jobs:
      submit: false
```

- Without `inherit: true` (including `inherit: false`), `perms.jobdb` retains its
  complete replacement semantics. Omitting `jobdb` still inherits, and `jobdb: {}`
  still denies all delegated capabilities.
- With `inherit: true`, start from the inherited grant and override only supplied
  fields. Omitted fields and empty mappings inherit. Explicit `false` disables
  an action; explicit `true` enables it within existing ceilings.
- Explicit secret and cell-name lists replace the inherited list. `secrets.resolve:
  []` removes delegated secret access. Cell-name lists must remain nonempty and
  require the resulting action to be enabled. Disabling an action clears its
  inherited cell-name filter; provide a new filter when enabling it again if needed.
- The resulting grant is inherited by descendants, which can override individual
  fields with `inherit: true` or replace the grant without it. Siblings recover
  their enclosing grant. The flag applies to this declaration only; it does not
  change the interpretation of descendant declarations.
- Existing limits still validate the resulting grant. Inherited default Git
  credentials remain resolved from each task's selected profile, including after
  resume; overrides must not freeze concrete secret names at an enclosing scope.
- Same-repository includes can inherit and override the current grant. A foreign
  include using `inherit: true` still needs an explicit `perms.limit.jobdb` at the
  callsite. Its inherited caller grant resets at that boundary; overrides apply
  to the callee defaults within that ceiling. Automatic Git credentials never
  authorize foreign secret inputs. Explicit secret overrides are checked against
  caller input authority and the include ceiling.
- Hook callsites can override the administratively authorized grant within its
  ceilings. Administrative hook configuration continues to require complete grants.
  Ordinary child jobs retain their independent root policy.
- Reject nulls, duplicate/unknown fields, malformed lists, and the superseded
  `perms.restrict` syntax. YAML and JSON round trips preserve explicit `false`
  and empty secret lists.
- `c2j self` reports effective permissions and explicit inherited denials under
  `permissions.inherited_denials` and on affected capabilities. Later enabling
  overrides and replacement grants clear those denial diagnostics.

## Acceptance

A task using the example can clone an authenticated repository using the configured
read-only Git profile, while child submission is rejected before creating a job.
Cover fresh and resumed tasks, two different Git-secret names, descendant partial
and complete grants that re-enable submission, unchanged sibling permissions,
list replacement/clearing, and explicit ceilings that still reject wider grants.
Cover default and explicit base grants, empty overrides, malformed declarations,
YAML/JSON round trips, and foreign includes that cannot be authorized by inheritance
alone. Verify both the operation guard and the issued delegated credential reject
submission before creating a job. Use synthetic credentials and the real
credential-helper and permission paths, without calling a model.
