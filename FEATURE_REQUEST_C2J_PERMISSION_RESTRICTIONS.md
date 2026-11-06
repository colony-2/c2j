# c2j: restrict individual permissions while inheriting the rest

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

Ordinary `perms.jobdb` declarations replace the inherited delegated grant. A
recipe granting `{x, y}` may contain a node granting `{x, y, z}` or just `{z}`.
Only explicit `perms.limit.jobdb`, include-boundary authority, and the owning
lease impose ceilings. Removing a capability from an ordinary grant does not
forbid a descendant from requesting it again.

## Proposal

Add a subtractive edit to the inherited grant:

```yaml
perms:
  restrict:
    jobdb:
      jobs:
        submit: false
```

- Omitted capabilities retain their otherwise effective permissions, including
  selected Git-profile credentials. Nothing becomes newly authorized.
- The edit only removes authority from the current grant; it does not create a
  ceiling. Descendants inherit the edited grant until an explicit `perms.jobdb`
  replaces it. That replacement may re-enable submission within existing ceilings.
- On the same node, apply `perms.jobdb` first, then `perms.restrict`. An empty
  restriction is a no-op. Existing limits still validate the resulting grant.
- Support `jobs.submit: false` and `cells.create: false` initially. Reject `true`,
  nulls, unknown fields, and list restrictions; complete grants remain the way to
  add capabilities or replace secret/name lists. Disabling an action also clears
  its associated cell-name filter.
- Inherited default Git credentials remain resolved from each task's selected
  profile; the edit must not freeze concrete secret names at an enclosing scope.
- Restrictions follow ordinary grant scope: siblings retain their original
  permissions, and include boundaries that reset a grant also reset its edits.
  Ordinary child jobs retain their existing independent root policy; hook child
  delegation retains its existing ceilings.
- Existing complete grants and limits keep their current semantics. A restriction
  alone does not authorize a foreign include or its secret inputs.
- `c2j self` reports effective permissions and the active restriction field that
  disables a capability. Replacing the grant also clears that diagnostic.

## Acceptance

A task using this overlay can clone an authenticated repository using the configured
read-only Git profile, while child submission is rejected before creating a job.
Cover fresh and resumed tasks, two different Git-secret names, successful
descendant replacement grants that re-enable submission, unchanged sibling
permissions, and explicit enclosing ceilings that still reject wider grants.
Cover full grants combined with edits, empty/malformed restrictions, YAML/JSON
round trips, and foreign includes that cannot be authorized by a restriction alone.
Verify both the operation guard and the issued delegated credential reject
submission before creating a job. Tests should use synthetic credentials and the
real credential-helper and permission paths, without calling a model.
