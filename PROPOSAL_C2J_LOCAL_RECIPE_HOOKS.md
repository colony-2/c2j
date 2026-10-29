# Proposal: repository-local recipe hooks with output contracts

Status: initial c2j implementation available. See [the user guide](GUIDE-Local-Recipe-Hooks.md)
for supported syntax, CLI commands, execution boundaries, and current limitations.
Build-specific checkpoint placement remains a consuming/shared-recipe change.

## Problem and approach

Projects use the shared build recipe from `colony-2/recipes`, but sometimes need
additional checks, reviews, or work without maintaining a customized copy of the
core workflow.

Provide an optional, repository-local include with a caller-owned contract. The
shared recipe declares an extension point, calls a local implementation when
present, validates its result, and decides whether to advance or revisit earlier
work.

The [earlier named-hook proposal](PROPOSAL_C2J_RECIPE_HOOKS.md) lets consumers
patch, replace, or insert nodes in a shared recipe graph. This proposal defines
explicit calls from the shared graph into project-supplied recipes. It can be
considered independently of graph patching.

## Ownership

This proposal fits the recipes cell's purpose. Implementation spans these owners:

- **Recipes:** hook locations, input/output and artifact contracts, revision
  routes, authoring guidance, and declarative behavioral tests.
- **c2j:** consuming-repository resolution, optional includes, output-boundary
  validation, durable snapshots, hook permission enforcement, and execution
  diagnostics.
- **jobdb:** authoritative configuration storage, delegated execution
  capabilities, and pluggable secret-provider authorization and retrieval.
- **Consuming cells:** local hook implementations and their application-specific
  behavior.
- **c2ops:** any new operation capabilities needed by a particular hook. The
  hook mechanism itself should compose existing operations.

## Hooks are recipe calls

The shared recipe owns:

- Where the hook runs.
- The inputs and artifacts it receives.
- Its expected output schema.
- The meaning of each result and the resulting control flow.
- The behavior when no implementation exists.

A consuming cell supplies an ordinary recipe at a conventional path, for example:

```text
.c2/recipes/hooks/build/implementation-ready.yaml
```

That recipe can run extra tests, consult another system, request review, or
perform additional work using existing ops and includes. It may delegate work
to child jobs through the ordinary lifecycle operations.

Start with one implementation per hook. Projects needing several checks compose
them inside that recipe and return one result to the shared workflow.

## Extend include semantics

Existing includes already expand into the durable job snapshot, enforce input
schemas, and export explicit outputs. See the
[inline include guide](INLINE_RECIPE_NODE_USER_GUIDE.md).

The proposed additions are:

1. An explicit reference to the consuming repository.
2. Defined fallback outputs when an optional recipe is absent.
3. Caller-side validation of the included recipe's published outputs.
4. Invocation-time resolution against the consuming workspace's pinned snapshot.
5. Hook permissions supplied by authoritative jobdb configuration.

These can be implemented as include capabilities, with “hook” describing their
use as a supported extension point. The authoring syntax need not introduce a
distinct node kind, but c2j needs durable runtime loading and expansion for these
calls; today's eager include expansion alone is insufficient. Existing shared
recipe includes can retain their current resolution semantics.

Illustrative callsite:

```yaml
project_check:
  include:
    recipe: cell://.c2/recipes/hooks/build/implementation-ready.yaml
    if_missing:
      outputs:
        decision: continue
        summary: No project hook configured.
        artifact_refs: {}

  inputs:
    candidate: ${{ context.git.hash }}
    documents: ${{ inputs.documents }}

  expect_outputs:
    type: object
    required: [decision, summary, artifact_refs]
    properties:
      decision:
        enum: [continue, revise_implementation, revise_design]
      summary:
        type: string
      artifact_refs:
        type: object

  transitions:
    - to: accept
      when: outputs.decision == 'continue'
    - to: implementation
      when: outputs.decision == 'revise_implementation'
    - to: design
      when: outputs.decision == 'revise_design'
```

This is a schematic boundary example, not a complete build integration. The
actual build must retain its assessment, verification, and approval gates on the
forward path. Production schemas must also validate artifact references using
c2j's artifact types; the object above only illustrates the contract's shape.
The proposed hook-entry protocol pins the candidate before evaluating the
`context.git.hash` input; that field can otherwise be empty in ref mode.

The parent owns transitions; the hook returns a decision. Local implementations
do not need to know internal state names. The expected schema belongs to the
caller, and the implementation cannot redefine what constitutes an acceptable
response. Fallback outputs must satisfy the same contract.

## Forward and reverse progress

Here, reverse progress means returning to earlier work for revision. An
implementation checkpoint could expose these results:

| Decision | Parent behavior |
| --- | --- |
| `continue` | Advance through the remaining standard gates. |
| `revise_implementation` | Pass findings to implementation, then repeat assessment and verification. |
| `revise_design` | Reopen design and invalidate approvals and evidence dependent on the previous design. |

Require useful feedback for revision results, with detailed findings supplied as
artifacts. For example:

```yaml
decision: revise_implementation
summary: The database migration cannot upgrade the previous release.
artifact_refs:
  migration-check.md: <artifact reference>
```

Each hook exposes only the decisions meaningful at its location. A design hook
cannot request revision of an implementation that does not yet exist. The
runtime validates generic schemas; build-specific decisions and their routing
remain recipe-owned behavior.

A crash or invalid output is an execution or contract failure, not a revision
decision. It follows ordinary error handling and must never silently become
`continue`.

Undoing external effects requires a separate compensation contract. Returning to
design does not automatically undo a deployment, publication, or other external
action. Automatic compensation is outside the initial proposal.

## Resolution and durability

### Repository identity and relative paths

`cell://` identifies the consuming cell repository even when the shared recipe
comes from `colony-2/recipes`. It is independent of `target_directory`.
Relative includes inside the hook resolve from the hook's own file.
They retain that file's source snapshot rather than independently following a
branch when reached. Explicit references to other repositories have their own
recorded source identities and ordinary permission boundaries.

The implementation must define the binding explicitly for recipes that switch
workspaces. For v1, prefer the job's consuming cell; another repository should
require an explicit reference rather than an implicit change in hook ownership.
Hook definition, candidate input, and execution must use the same consuming
workspace snapshot. A call from another active workspace must explicitly enter
the consuming workspace or fail; it must not silently combine one repository's
hook with another repository's candidate.

### Resolution timing

Resolve each `cell://` hook when execution arrives at its callsite. A workflow
can remain in ref mode through any number of operations that make no changes;
having observed `resolved_hash` does not itself pin subsequent execution.
Loading a hook at job construction and later following a moving ref would allow
the hook definition and application code to come from different snapshots.

Hook entry must durably establish the following before executing hook code:

1. Select the consuming workspace's current candidate. If it follows a floating
   ref, resolve and pin that ref. If it already has a persisted candidate, use
   that candidate, including its thin-pack-backed changes, rather than its older
   base commit. A literal full commit hash is already stable.
2. Read the hook from that exact snapshot and record its source revision,
   content identity, and replayable definition, or its absence at that snapshot.
3. Validate the hook and its permission requests against the recorded project
   policy described below, then evaluate candidate-dependent inputs.
4. Execute against the pinned workspace and propagate that pin to subsequent
   work in the same workspace lineage. Normal operations may produce newer
   candidate snapshots from it.

Persist the pin and resolution record before dispatching hook operations so
retries cannot combine a saved definition with a newly resolved branch tip.
Pinning remains effective when the hook is absent or makes no changes; it must
not require an empty commit or thin pack. Keeping the original branch for
publication or diagnostics must not cause checkout to follow it again.

Definitions are fixed per invocation. A later checkpoint visit may load a hook
created or changed by earlier work, from that visit's candidate snapshot.
Snapshot consistency does not authorize that changed code to access secrets.

### Missing versus broken

Only a genuinely absent optional file uses fallback outputs. Invalid YAML,
failed repository lookup, unsupported contracts, and invalid returned outputs
must surface as errors. Absence should be visible in the job story rather than
reported as a check that ran successfully.

### Repeated visits and resume

Run the hook on every visit to its checkpoint. Durable resume preserves completed
work according to normal execution semantics; a new candidate or revision visit
creates a new invocation.
Once an invocation has recorded its candidate and hook definition or absence,
retry and resume reuse them. A new visit gets a new resolution record; it does
not re-resolve a floating branch if its workspace lineage is already pinned.

Hooks with external effects need a stable invocation identity for deduplication:
retries of one invocation retain that identity, while a new visit receives a new
identity. This does not imply automatic exactly-once execution of external
systems.

## Hook permissions and secrets

### Authoritative jobdb configuration

Store hook policy exclusively as versioned, structured jobdb configuration
values using the tenant/cell layout below. There is no `.c2/hook-perms` file,
repository policy fallback, or separate c2j policy store. These values contain
authorization policy and secret names, never secret values. They need not be
confidential, but only authorized configuration administrators may change them.

This follows the current Git profile arrangement: profile documents live in
jobdb configuration and refer to separately stored secrets. c2j interprets the
policy; jobdb owns storage and configuration administration.

The policy must bind grants to verified consuming-repository identity and a
specific hook identity/path, with explicit source-trust conditions. A hook
declares the permissions it needs; the shared recipe does not need to enumerate
every consuming project's requirements. Missing policy or no matching grant
permits no hook JobDB capabilities, including secret access and job submission.
Malformed policy and configuration lookup failures are errors, not permission
to fall back to broader access.

Hook configuration supports the complete recipe `perms` schema and semantics,
including `perms.jobdb` grants and `perms.limit.jobdb` ceilings. Currently those
capabilities are `jobdb.secrets.resolve` (exact secret names) and
`jobdb.jobs.submit` (boolean). Reuse the recipe permission types and validators;
do not maintain a smaller, secrets-only permission language for hooks. Future
recipe permission capabilities must also be supported by hook configuration,
with unsupported fields rejected by older runtimes. This parity does not add
JobDB configuration access: it is not a recipe-delegable capability today.

### Tenant defaults and cell overrides

Use one tenant-default key and one key per consuming cell repository, all inside
the selected jobdb tenant's existing configuration namespace:

| Key | Contents |
| --- | --- |
| `_c2j_hook_policy_tenant` | Versioned defaults indexed by hook path, applying to every consuming cell in this tenant unless overridden. |
| `_c2j_hook_policy_cell_<repository_digest>` | Versioned overrides indexed by hook path for one canonical consuming repository; also stores the full repository identity. |

Both documents contain independent entries per hook. Tenant-wide means a default
for one specified hook across cells, not a global grant shared by all hooks.
The storage keys group those entries; they do not define the permission boundary.

`repository_digest` is the lowercase SHA-256 hex digest of the UTF-8 canonical
repository identity. CLI and runtime must use the same identity routine as the
permission resolver. Resolve short cell names through project cell resolution
first; do not key permissions by a display name, branch, current directory, or
active workspace label. Retain the canonical identity in the value and verify
it against the requested repository and key when loading. Do not silently equate
SSH/HTTPS aliases or local clones with a remote repository unless the common
identity implementation explicitly supports that equivalence. An unresolved
identity is an error, not a reason to use a same-named cell's policy.

For v1, cell policy scope is repository-wide, consistent with `cell://`
ownership. Two aliases for the same canonical repository share policy. Distinct
subdirectory cells in one repository would require a later explicit identity
extension; `target_directory` does not create another permission scope.

Hook entries use exact, normalized repository-relative paths, for example
`.c2/recipes/hooks/build/implementation-ready.yaml`. A logical name such as
`build.implementation_ready` may be shown for diagnostics, but cannot substitute
for checking the resolved source path. Reject escaping paths and ambiguous
aliases, including symlinks that change the authorized source.

Resolve each hook's policy from one coherent configuration snapshot:

1. If the cell document contains that hook path, use its complete entry.
2. Otherwise, use the tenant document's entry for that hook path.
3. If neither contains an entry, permit no hook JobDB capabilities.

There is no union of tenant and cell grants. An explicit cell entry with empty
permissions denies access even when tenant defaults grant it. Absence means
inheritance; an empty entry means deny. Overrides are per hook, so configuring
one cell hook does not suppress defaults for unrelated hooks. Tenant defaults
are defaults, not a mandatory ceiling: a configuration administrator may give
a particular cell additional access. Existing job-wide limits and provider
authorization still apply. If a tenant-wide mandatory ceiling is needed later,
it must be a separate, explicit policy concept.

Each hook entry contains the standard recipe `perms` object plus source-trust
conditions attached to individual grants. Secret conditions are per exact name;
job-submission permission has its own condition. Adding a candidate-trusting
grant must not weaken another permission's stricter source condition. Limits
narrow authority using the same semantics as recipe limits. An illustrative
cell document is:

```json
{
  "version": 1,
  "repository": "https://github.com/example/service",
  "hooks": {
    ".c2/recipes/hooks/build/implementation-ready.yaml": {
      "perms": {
        "jobdb": {
          "secrets": {"resolve": ["TEST_TOKEN"]},
          "jobs": {"submit": true}
        }
      },
      "source_trust": {
        "jobdb.secrets.resolve": {"TEST_TOKEN": {"mode": "candidate"}},
        "jobdb.jobs.submit": {"mode": "candidate"}
      }
    },
    ".c2/recipes/hooks/build/plan-ready.yaml": {
      "perms": {"jobdb": {}, "limit": {"jobdb": {}}}
    }
  }
}
```

The tenant document has the same version and hook-entry structure without a
single-repository binding. Tenant-wide candidate-trusting grants intentionally
cover candidate code in every consuming cell and should be reported as such by
the CLI. Unknown versions, malformed entries, and identity mismatches fail
closed. Validate the relevant tenant and cell documents before selection;
broken overrides must never become inheritance. Treat each selected hook entry
as an explicit authorization boundary: omitted capabilities are denied, and a
limit alone does not create a grant. An empty entry must not activate the normal
operation defaults for Git credentials or job submission.

This layout keeps cell updates to one configuration value and lets the CLI list
configured cells by scanning the reserved prefix in an administrative snapshot.
It consumes one configuration entry per configured repository and remains subject
to jobdb entry, value, and total-snapshot limits. Separate keys do not create
per-cell administrative ACLs: management still requires the selected tenant's
configuration-administration authority. jobdb revisions cover the tenant
snapshot, so concurrent edits to different cell keys can still conflict.

### Proposed permission-management CLI

Add `c2j hooks perms` as a provisioning command group. These commands are
implemented in c2j. Follow `c2j git` management conventions for
`--jobdb URI`, `--embed`, configured endpoint discovery, administrative access, and
revision-checked writes. `--jobdb` selects the jobdb tenant; `--tenant-wide`
selects the default policy inside that tenant.

Require exactly one of `--tenant-wide` or `--cell CELL` for scoped commands.
`CELL` accepts a configured short name or explicit repository reference. Never
infer tenant-wide mutation from an omitted cell. Print the selected tenant,
canonical repository where applicable, hook path, and resulting scope.

| Command | Behavior |
| --- | --- |
| `c2j hooks perms add HOOK [--secret NAME ...] [--submit-jobs] SCOPE TRUST` | Add selected secret and/or job-submission grants. Require at least one capability and an explicit source-trust choice for each new grant. |
| `c2j hooks perms remove HOOK [--secret NAME ...] [--submit-jobs] SCOPE` | Remove selected secret and/or job-submission grants from the effective hook entry; require at least one capability. |
| `c2j hooks perms set HOOK --policy-json JSON SCOPE` | Replace this hook entry with a complete `perms` object and grant-specific source-trust conditions; `--policy-json -` reads JSON from stdin. Supports the full recipe permission schema, including limits. |
| `c2j hooks perms clear HOOK SCOPE` | Store an explicit empty hook entry, denying all hook JobDB capabilities at that scope. |
| `c2j hooks perms inherit HOOK --cell CELL` | Delete only this cell's hook override and restore tenant-default inheritance. |
| `c2j hooks perms get HOOK SCOPE` | Show the stored entry, effective grants and trust conditions, configuration revision, and whether the entry is inherited, overridden, or denied. |
| `c2j hooks perms list SCOPE` | List hooks and their effective grants with provenance for that scope. |
| `c2j hooks perms list --all-cells` | List configured cell overrides and tenant defaults in the selected tenant. |

Here `HOOK` is the repository-relative hook path and `SCOPE` is one of the scope
flags above. For the initial CLI, `TRUST` is exactly one of
`--trust-candidate-code` or `--candidate-commit FULL_SHA`. The first explicitly
trusts code in the invocation's candidate with those capabilities. The second
requires the invocation candidate to equal that immutable commit; it does not
load a hook from the named commit against different candidate code. This is a
deliberately narrow starting point; richer independently approved source policies
can be added later. External recipe, op, and CEL package references accept
branches and tags under the ordinary resolution rules. Granting secrets or other
permissions does not require authors to supply full commit SHAs. Same-repository
includes inherit permissions; foreign includes require explicit delegation.
Hooks and nested includes can declare imported CEL function packages just like
other recipes. Resolve their floating package references at hook arrival and
record the result for retries and replay. Any future changes to CEL package
execution must apply consistently to hooks and ordinary includes.
`set` uses trust conditions in the supplied entry and replaces only the selected
hook; it does not require a separate trust flag or `--replace`. Its JSON input is
an administrative update to jobdb configuration, not a repository policy source.
Convenience flags cover today's capabilities; `set` must support every field
accepted by the shared recipe permission schema.

Examples (assuming the jobdb endpoint is already configured):

```sh
# Default for this hook across all cells in the selected tenant.
c2j hooks perms add .c2/recipes/hooks/build/implementation-ready.yaml \
  --tenant-wide --secret TEST_TOKEN --trust-candidate-code

# Add a cell-specific secret, preserving the currently effective grants.
c2j hooks perms add .c2/recipes/hooks/build/implementation-ready.yaml \
  --cell service --secret SERVICE_TEST_TOKEN --trust-candidate-code

# Remove an inherited permission for this cell without re-enabling defaults.
c2j hooks perms remove .c2/recipes/hooks/build/implementation-ready.yaml \
  --cell service --secret TEST_TOKEN

# Permit this particular hook to submit child jobs.
c2j hooks perms add .c2/recipes/hooks/build/implementation-ready.yaml \
  --cell service --submit-jobs --trust-candidate-code

# Inspect the resulting override, then explicitly restore inheritance.
c2j hooks perms get .c2/recipes/hooks/build/implementation-ready.yaml --cell service
c2j hooks perms inherit .c2/recipes/hooks/build/implementation-ready.yaml --cell service
```

Cell `add` and `remove` first materialize the current effective hook entry if
there is no override, then apply the edit and store the full resulting override.
That copied entry no longer follows future tenant-default changes. `get` and the
mutation result must make this visible. Removing the final capability leaves an
explicit empty override; it must not delete the hook entry. Only `inherit`
restores fallback. For example, if the tenant grants A and B, removing A for a
cell leaves an override containing B. Later adding C to the tenant defaults does
not add C to that cell's override.

At tenant scope, `remove` and `clear` modify only defaults; they do not revoke
grants already present in cell overrides or durable job policy snapshots. Report
that scope in command output. `inherit` likewise may increase access and must
show the resulting effective grants. Immediate credential revocation remains a
secret-provider responsibility.

Make repeated identical edits idempotent. Re-adding an existing capability with a
different trust condition requires an explicit `--replace` flag, so an ordinary
add cannot silently weaken its condition. `add` and `remove` edit the requested
grants without changing unrelated hooks, capabilities, limits, or trust
conditions. `clear` needs neither capability selectors nor a trust flag.

For each mutation, read one administrative configuration snapshot, compute the
new target document, validate it and configuration size limits, and write it
with `ExpectedRevision`. Fail on a revision conflict and require rereading;
never silently overwrite concurrent edits or recompute a broader grant during
an automatic retry. A command edits one key atomically. Deleting the final cell
override via `inherit` may remove the cell key with the same revision check.
Do not offer a bulk multi-key mutation until its partial-failure semantics are
defined. Management commands must reject execution/delegated contexts without
discovering or borrowing provisioning credentials, as Git profile management
does. No command resolves or prints secret values.

### Controller access and delegated tokens

The trusted c2j controller reads policy using its owning execution lease. Hooks
and operations receive restricted capabilities, not the controller's lease or
configuration-administration credentials.

In the current jobdb delegation API, configuration reads are not an available
delegated permission, and omitted actions are denied. Configuration writes
require separate `ConfigurationAdmin` authority; even an ordinary execution
lease is not configuration-admin authority. Thus delegated tokens authorize
neither configuration reads nor writes. This design does not add either power.
Controller-provided policy diagnostics do not imply direct configuration access.

Capture both relevant policy documents (including recorded absence), their
effective selection, and configuration revision durably during trusted
job preparation, before running recipe code, and reuse that policy on resume.
jobdb's workflow configuration cache lasts for one live job attempt and is not
itself a durable policy snapshot. Record the policy identity and effective grant
for each hook invocation; never persist resolved secret values in that record.
Configuration changes apply to newly prepared jobs. Revocation at the secret
provider remains authoritative for subsequent provider requests.

### Enforcement and provider integration

c2j checks the hook's requested capabilities against project policy, its source-trust
conditions, and applicable execution ceilings, then issues only the required
restricted capabilities. Input secret materialization and direct secret
resolution must both enforce the hook boundary, as must job submission. A
configuration-authorized callback grant belongs to the hook invocation; it does
not grant those capabilities to the shared caller or silently lift ordinary
include ceilings. The controller
must explicitly establish that callback authority, while preserving job-wide
limits and explicit restrictions on the call.

Keep the existing jobdb secret provider as the final authorization and retrieval
boundary. A c2j grant cannot override a provider denial. Provider requests already
carry tenant/job identity, verified principal and lease information, and stored
job `AppMetadata`. That metadata describes the job, not the currently executing
include, and storage alone does not establish the truth of submitter claims.

For v1, c2j enforces hook-specific policy and providers retain their existing
job-level authorization. Independent provider enforcement of hook identity
requires a future controller-established invocation context bound to the
request or capability: repository, hook path/content identity, candidate,
policy identity, and execution lineage. Recipe-supplied metadata must not stand
in for those trusted facts. Secret caches must not bypass invocation permission
checks or reuse a value under authority broader than the current invocation.

### Candidate edits and child jobs

External policy prevents a workflow from editing its own grants, but does not
make code at an allowed path trustworthy. A workflow can change a permitted
hook, or a script it invokes, and reach that hook in the same job. A path-only
grant must not implicitly authorize candidate-authored code.

Policy must explicitly choose whether it trusts candidate code with the granted
capabilities or requires independently authorized source/content. That trust
covers the code and dependencies using those capabilities, not just hook YAML.
If the selected trust condition is not met, capability access fails; the runtime
must not silently load an older hook against the new candidate to satisfy it.
The candidate-code and exact-candidate-commit conditions above are the initial
choices. Their verification, including dependency handling, must be settled
before enabling secret-bearing hooks.

Starting a child job must not reset the permission ceiling or make modified
candidate code trusted. Propagate authority and policy provenance through a
trusted submission path, or independently authorize the child's execution.
An explicit callback grant may authorize additional hook access, but arbitrary
child submission or child metadata cannot manufacture that grant. Permission
to submit a job does not by itself establish the submitted job's permissions;
this propagation requires explicit implementation and coverage. Nested
cross-repository includes also retain explicit delegation boundaries.

## Inputs, artifacts, and candidate changes

Pass a small, documented input interface: the candidate, relevant documents,
and previous feedback where needed. Avoid exposing the entire internal `flow`
object or requiring access to surrounding state identifiers.

Export hook artifacts explicitly into later reviews and the final result.
Preserve their originating artifact references, and define how naming conflicts
with existing documents are handled rather than silently overwriting evidence.

If a hook changes code or reviewed documents, previous checks may become stale.
Place mutating hooks before assessment, or have the parent rerun affected gates
when the candidate changes. A `continue` result alone cannot establish that old
verification or approval still covers a modified candidate.

## Initial build extension points

Start with two deliberate extension points in
[`recipes/develop/develop.yaml`](../recipes/develop/develop.yaml):

- **`build.plan_ready`:** after design and test statements are prepared, before
  assessment and human approval.
- **`build.implementation_ready`:** after implementation is prepared, before
  assessment, verification, and outcome acceptance.

These placements let project-specific work participate in the existing review
cycle. Missing hooks mean there are no additional project requirements at that
checkpoint; they do not replace or waive standard verification.

Hook placement, inputs, allowed decisions, artifact handling, and revision
behavior form a supported interface. Changes to those meanings require an
explicit compatibility decision even when the result schema is unchanged.

## Initial scope and acceptance coverage

The first implementation should support explicit local calls, invocation-time
snapshot resolution, jobdb-configured hook permissions, validated results, and
parent-owned revision routes. General graph patching, multiple automatically
discovered implementations, replacing definitions within an existing invocation,
provider-side invocation identity enforcement, and automatic compensation can
remain separate proposals.

Before implementation, define test statements covering:

- Present hooks execute and publish results and evidence.
- Absent optional hooks produce schema-valid fallback outputs and visible absence.
- Broken hook files and lookup failures do not use the missing-file fallback.
- Invalid decisions and output shapes fail before parent transitions execute.
- Revision feedback reaches the appropriate phase and invalidates dependent gates.
- Forward results preserve standard assessment, verification, and approval.
- Hook paths resolve in the consuming repository and nested includes remain local
  to their source recipe.
- A moving branch between job creation and hook entry cannot produce a mismatch
  between the hook definition and execution snapshot.
- Pinning precedes candidate input evaluation and survives absent/read-only
  hooks and later workspace preparation, without empty commits or thin packs.
- Persisted candidate edits to hooks are visible on new visits; nested relative
  includes retain their enclosing source snapshot.
- Retry and resume use the recorded candidate, hook definition or absence, and
  policy; a crash after pinning cannot cause a second branch resolution.
- New checkpoint visits execute again, while retries retain invocation identity.
- Candidate changes cannot reuse stale verification or approval.
- Missing grants deny secret access; malformed/unavailable policy fails closed.
- Hook entries accept the full recipe `perms` schema, including secret grants,
  job submission, and limits; an omitted capability cannot regain operation
  defaults, and both schema validators reject the same unsupported fields.
- Tenant defaults remain per hook: granting one hook secret access or job
  submission cannot grant either capability to another hook.
- Only jobdb configuration supplies policy; repository files cannot grant access.
- Delegated tokens cannot read or write configuration, while trusted controller
  preparation can read it without exposing the owning lease.
- Policy survives a new job attempt even if live configuration changes.
- Both input materialization and direct resolution enforce the hook grant;
  cached secrets and shared callers cannot bypass it.
- Provider denial remains effective despite a recorded c2j grant.
- Editing a hook or its invoked code cannot acquire secret access without the
  configured source-trust condition, within either the same job or a child job.
- Child jobs preserve authority restrictions or receive independent
  authorization; forged lineage metadata cannot elevate access.
- Tenant defaults and cell overrides resolve per exact hook path; an explicit
  empty cell entry denies access rather than falling back or unioning grants.
- Cell identity uses the canonical repository: aliases share only when that
  identity matches, and same-named repositories cannot share grants accidentally.
- Cell `add`/`remove` materialize effective policy, preserve unrelated grants and
  trust conditions, and retain empty overrides; only `inherit` restores defaults.
- CLI scope selection is explicit, trust choices are required for new grants,
  trust changes require `--replace`, and management cannot use delegated tokens.
- CLI `set` accepts complete permission entries and limits; `add`/`remove` support
  job submission as well as secrets and preserve unrelated capabilities.
- Policy mutations enforce expected revisions and configuration limits; a
  concurrent edit cannot be overwritten or silently broaden the computed grant.
- `get`/`list` and mutation results expose scope, effective grants, inheritance,
  and policy revision without resolving secrets.

Declarative workflow tests belong in this cell. Resolver, boundary-validation,
and persistence tests belong with the c2j implementation. Exact YAML field names,
schema representation, policy/trust-condition representation, callback and child
authority propagation, and snapshot metadata should be agreed with c2j and jobdb
before turning this proposal into executable recipes.
