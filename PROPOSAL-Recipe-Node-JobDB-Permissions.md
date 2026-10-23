# Recipe node JobDB permissions

Status: c2j implementation with opt-in migration, 2026-10-23. Remote execution
awaits the JobDB deadline accessor in [the upstream patch](JOBDB-TASK-DEADLINE.patch). See
[the usage guide](GUIDE-Node-Permissions.md) for the runtime contract and rollout.

Add a `perms` mapping to common recipe node metadata, with a nested `jobdb`
namespace. It describes what the
operation may do, including through c2j helpers. The trusted workflow controller
keeps the owner lease and gives each live invocation only its approved authority.
Codex can receive its model credential; a publishing node can receive a separate
repository write credential. Neither implicitly receives the other's authority.

Base this on the implemented [delegation contract](PROPOSAL-Restricted-Lease-Capabilities.md)
and [integration guide](GUIDE-Lease-Delegation-Tokens.md). Recipe policies are
requests, not authorization: deployment policy and enclosing security limits
decide what the controller may grant.

The JobDB restriction that disabled renewal with `notAfter` was a bug, now fixed in JobDB main. This proposal targets renewal within an immutable hard deadline.
JobDB main now supports local attenuation. All issuance goes through one c2j
integration boundary, which uses that API without changing recipe semantics.

## Default policy

| Operation access | Default |
| --- | --- |
| Resolve secrets | Default Git RO profile credentials at the root, inherited by ordinary nodes; default Git RW profile credentials on designated publishing nodes; no other secrets |
| Submit jobs | Allowed within the lease's tenant; no target-specific restrictions in v1 |
| Read/write chapters | None |
| Read configuration | Allowed through driver resolution or the existing execution config interface; no per-node read limits |
| Write JobDB configuration | None |
| Named config references in recipe fields | Driver resolves automatically, including in cross-repository includes; no config-read permission declarations |
| Download artifacts through JobDB | None directly |
| Acquire, complete, restart, reschedule, update client payload, administer schemas, or delegate | None |

Submission is permission to put a message on a queue. The receiving job runs
under its own recipe and authorization policy, independently of the submitter's
permissions. Every submitted job remains lease-scoped to its actual parent for
JobDB parent tracking; that relationship does not propagate permission limits.
A root limit can default submission off. Cell, recipe, and other target
restrictions are a future improvement.

Normal inputs, declared input artifacts, outputs, and result persistence remain
available through the trusted task protocol. They do not require arbitrary
chapter access. The controller handles lifecycle, input preparation, and durable
outcome writes. Permission defaults apply to operation-visible authority, not to
the controller's internal work.

## Proposed authoring model

Group permissions by system under `perms`, starting with `jobdb`. Use nested
recipe keys rather than dotted or underscore-separated permission names. Future
systems can add namespaces alongside `jobdb`. Reserve `limit` under `perms` for
subtree ceilings, using the same system namespaces and nested permission shape.
For example, grants live under `perms` → `jobdb`, and ceilings live under
`perms` → `limit` → `jobdb`.

Within `jobdb`, `secrets` → `resolve` is an exact secret-name list and
`jobs` → `submit` is a boolean. Git profile selection stays in `git_profile`;
Git profiles are not JobDB permissions. The issuer translates this recipe syntax to JobDB's wire
permission names. Existing unrelated recipe fields such as `git_profile` and
`input_schema` retain their current syntax; this proposal introduces no new
dotted or underscore-separated recipe keys.

```yaml
id: change-and-publish
version: "1.0.0"
input_schema: {}
sequence:
  - id: edit
    git_profile: read
    perms:
      jobdb:
        secrets:
          resolve: [codex-key, repo-read-key]
        jobs:
          submit: true
    op: command_execution
    inputs:
      env:
        OPENAI_API_KEY: '${{ context.secrets["codex-key"] }}'
      run: codex exec 'Implement the requested change'

  - id: publish
    op: merge

  - id: inspect_local
    git_profile: null
    perms:
      jobdb: {}
    op: command_execution
    inputs:
      run: git status --short
outputs: {}
```

The command is illustrative; the same metadata applies to a selector-backed
Codex op. Assume the default RO and RW configuration select profiles `read` and
`publish`. The publish node automatically gets the RW profile credentials and
the default submission permission, but no model credential. The local inspection
node gets no operation-visible JobDB access. Without an explicit policy, the
recipe root and ordinary nodes automatically get the configured RO credentials.

The secret name list matches exactly, without wildcards. Explicit grants name
concrete JobDB secrets, including any Git credentials the node needs. Profile
selection and URL matching remain Git helper concerns. The driver expands the
selected default profile into concrete secret names only when computing implicit
permissions. There is no Git-profile selector in `perms.jobdb`.

### Automatic Git credentials

At root admission, the driver resolves `git-profile-ro` and its validated profile
record. Its exact credential references form the default root secret grant and
are inherited by ordinary nodes. Resolve actual values lazily under the effective
node authority, preserving existing secret lifetime and redaction behavior. Do
not put plaintext credentials into the recipe, template variables, or snapshots.

For built-in `merge` and `squashrebasemerge`, use `git-profile-rw` and make its
credentials available for that node. Preserve the existing Git selection rules:
an explicit enclosing `git_profile`, including null, overrides implicit defaults;
the root's implicit RO default does not suppress the publishing-node RW default.
The default Git credential contribution follows the selected profile, replacing
the implicit RO contribution for that publishing node. Restore the enclosing
selection/grant on exit. An ordinary Codex or shell node never gets the RW
default because its command happens to run `git push`.

The driver identifies designated publishing operations by trusted built-in
registration, not an extension's claimed name. Missing/null default configuration
adds no credentials; invalid profiles fail preparation. Explicit `git_profile`
selections in trusted recipe code can select another profile for the automatic
Git contribution, subject to deployment policy and all enclosing limits.
Subprocess calls to `git activate` cannot expand the admitted selection or grant.

These are implicit defaults, not additions that override an explicit policy.
An explicit `perms.jobdb` mapping replaces the inherited/default operation
policy, including automatic Git grants. The edit example therefore includes
the concrete Git RO secret alongside its model secret. An inherited explicit policy
also suppresses implicit widening on publishing descendants; override that policy
explicitly if those descendants need RW. An empty `jobdb` grant stays deny-all
for JobDB operation access.
`git_profile: null` suppresses the automatic Git contribution, while other
permissions keep their normal inheritance. A selected profile with no authorized
credential must not be served by a broader resolver.

All default Git grants are bounded by `perms.limit.jobdb`, including a cross-repository
include's explicit ceiling. A foreign recipe containing `merge` cannot acquire
RW unless the caller allowed the RW secret. Reading profile
configuration is unrestricted by this policy; resolving its secret references
still requires the corresponding credential grant.

Trusted bootstrap may fetch recipes before their root policy is known, using
separately approved RO authority. Once the root is admitted, its RO credential
grant makes ordinary operation-level reads such as `git fetch` work without
extra permission statements. Local editing needs no Git credential.

Profile URL matching is helper behavior, not confinement of a disclosed token.
The backing Git credential must itself be restricted to the intended repository
and access level. Granting a secret permits the operation to learn its value;
“Git helper only, never reveal the credential” would require a different proxy
design. Publishing must also avoid executing workspace-controlled hooks or
scripts with the publishing credential unless that execution is intended.

## Inheritance, addition, and removal

Use replacement semantics to keep effective authority easy to review:

- Omitted `perms.jobdb` inherits the enclosing policy; at the root it uses
  the documented default.
- An explicit mapping replaces the entire inherited policy. Omitted permissions
  mean none. `jobdb: {}` under `perms` removes all JobDB operation permissions.
- Replacement is per system namespace: setting `jobdb` does not reset future
  sibling namespaces. `perms: {}` names no systems and therefore adds no override.
  The reserved `limit` entry sets ceilings and never grants authority by itself.
- A descendant can request additional permissions, subject to its security
  ceiling. A sibling never inherits the preceding sibling's grant.
- Apply the field to roots, sequences, states, ops, includes, and child groups.
  Shared nodes take metadata from their definition or a containing node, matching
  the existing shared-node model.

This supports adding/removing authority by replacing an allowlist. Avoid separate
`add`/`remove` operators in v1: mixed implicit unions and subtraction make it hard
to see whether a sensitive credential survives a nested override.

Inheritance alone is not a security boundary. Add `perms.limit.jobdb` with
the same nested operation policy shape for a
subtree that must not regain permissions. It is optional inside one repository
and required at a cross-repository include call site. Where optional, an omitted
limit adds no constraint; an explicit limit is a complete allowlist and
intersects all enclosing limits. An empty `jobdb` limit is a permanent deny-all
ceiling for JobDB operation access within that subtree. A descendant recipe node
cannot reset or enlarge a limit. This
lexical limit does not cross a separately submitted job boundary.

For example, a wrapper around an included coding recipe can allow the model key,
the read secret, and submission permission in `perms.limit.jobdb`. Any
nested request for the write secret fails, even if that include was authored
with broader permissions. Include bodies and call-site limits must both survive
expansion. Apply this check to reused/shared definitions within the same job.
For a child group, the limit governs its submission work; the submitted jobs
have independent policies.

Conceptually:

```text
ceiling = deployment policy for this job ∩ lexical ancestor/include limits
request = nearest explicit perms.jobdb, otherwise defaults within ceiling
effective = request, after resolving selectors and proving request ⊆ ceiling
```

Implicit defaults add only authority allowed by the ceiling; explicit requests
outside it fail before side effects. A missing default grant cannot be supplied
through the owner's authority when an operation tries to use it. Check provider
authorization as well when resolving secrets. Optional-secret fallbacks retain
their ordinary missing/denied-value behavior after policy admission.

Start with literal permission names; do not evaluate permission
names from model output, arbitrary CEL, or task inputs. Forwarded secret inputs
are checked against the resolved node allowlist, not automatically authorized.
An extension manifest can describe required secrets but cannot grant them.

## Driver config references

Named config references in recipe fields are resolved by the driver without
permission declarations or config-name allowlists. This applies equally to root
recipes, same-repository includes, cross-repository includes, and extension
manifest defaults. There is no config-read restriction in this proposal.

```yaml
op: command_execution
inputs:
  env:
    RELEASE_CHANNEL: '${{ context.config.release_channel }}'
  run: ./build
```

Preserve existing reference syntax, permitted destinations, lazy materialization,
fallbacks, and replay semantics. The driver passes resolved values through the
ordinary input channel. Empty operation grants or include limits do not disable
config references. Config values are non-secret; credentials remain subject to
secret permissions even when configuration contains their names or profile records.
Reading a Git profile definition does not authorize its credentials.

JobDB delegated tokens currently have no configuration-read capability. Driver
resolution does not add a new token permission or give operations the driver's
owner credential. This is a transport distinction, not a per-key config policy.
The proposal adds no config authorization checks to compilation or materialization.
Existing execution-interface reads, such as `c2j config get`, can remain
driver-mediated without a config permission declaration. Keep normal execution
lifetime checks; no config-name allowlist is introduced for these reads either.

## Cross-repository operation boundaries

For inclusion from outside the current recipe's repository, require a complete
caller-authored JobDB permission limit at that include site. For example
(proposed syntax):

```yaml
- id: external_review
  include: git+https://github.com/example/review-recipes.git//review.yaml@main
  perms:
    limit:
      jobdb:
        secrets:
          resolve: [codex-key, repo-read-key]
        jobs:
          submit: false
```

The included recipe's operation requests and implicit Git defaults stay within
this limit. It cannot obtain RW by selecting the publishing profile, calling a
built-in publishing op, or adding a broader grant. Omitted entries in this
complete ceiling deny operation access, including submission. An empty JobDB
limit explicitly permits no JobDB operation capabilities. A missing limit on a
foreign include is a validation error identifying the include and resolved source.
Config references remain unrestricted by this limit.

Reset inherited operation grants at this boundary: the foreign subtree starts
with implicit defaults bounded by the call-site limit, then applies its own
explicit requests within that limit. Caller-selected grants can be passed
explicitly at the call site, also subject to the limit. No automatic root RO or
publishing RW grant can exceed the boundary. Loading another recipe root does
not give that root the outer job's unrestricted operation defaults.

Determine repository identity from the trusted resolver's canonical source
identity, not an include's spelling, recipe ID, workspace cell, or claimed
metadata. Pin the resolved commit and retain source provenance through inline
expansion, shared definitions, and compiled snapshots. Same-repository includes
inherit normally. URL aliases need trusted normalization; unknown origins,
server/artifact recipes without verified provenance, and paths escaping the
source repository are external for this purpose. A different ref in the same
repository retains the repository trust model but is pinned for execution.

Nested foreign includes need their own explicit call-site limits, intersected
with every enclosing limit. Including code back from the original trusted repo
does not escape an active ceiling. Install the operation boundary before
credential-bearing preparation or execution; expanding an include must preserve
its limit. These checks do not constrain driver-side config references.

These boundaries apply to inline recipe inclusion within the same job. Separate
job submission retains the queue semantics below and does not propagate limits.

## Submission is a queue permission

The nested `submit` field under `perms` → `jobdb` → `jobs` is a boolean in v1.
`true` compiles to the following existing JobDB wire grant (dotted names here
belong to JobDB's API, not recipe keys):

```json
{
  "jobs.submit": {
    "jobTypes": {"any": true},
    "jobIds": {"any": true}
  }
}
```

An explicit `false`, or omission within an explicit `perms.jobdb` mapping,
omits the grant. Generated and explicit IDs are both allowed when enabled.
Normal JobDB tenant, schema, parent-liveness, and submission validation still
apply. This does not grant restart submission or inline schema registration.

There is no cell, recipe, repository, or job-type target restriction in this
initial policy. Specific target permissions can be added later without changing
the meaning of the boolean grant. c2j's child broker may remain as the existing
submission transport, but it must honor this boolean and use the restricted
authority rather than bypass a denial with the owner lease.

A submitted job does not inherit the submitting node's `perms.jobdb` or
`perms.limit.jobdb`. It acquires its own lease and its nodes receive permissions under
that job's recipe and deployment policy. An RO agent may enqueue work that a
separately authorized publisher performs with RW credentials. No child admission
envelope, transitive authority ceiling, or special privileged-service permission
is introduced by this proposal. What the receiver does with the message belongs
to the receiver's policy.

## Runtime enforcement and task lifetime

### One delegation issuance boundary

Centralize all restricted credential creation behind a small c2j runtime service,
for example `IssueRestrictedLease(ctx, parent, resolvedGrant)`. No compiler, op,
Git helper, child broker, or CLI path calls JobDB delegation creation directly.
The compiler resolves policy; this service is the sole credential issuer.

The issuer receives the exact JobDB grant and effective absolute hard deadline.
Invocation identity stays in the trusted task context; JobDB owns transport refresh. Keep source
provenance and default Git credential expansion in c2j policy evaluation;
only supported concrete permissions cross the JobDB credential boundary. Return
the existing restricted lease/capability abstraction so callers do not depend
on how it was issued.

The service calls JobDB’s local attenuation API. Future call-pattern changes
stay inside that implementation. Do not invent a token format or copy signing logic into c2j.
The central service must validate that requested authority fits both c2j's
resolved policy and the supplied parent capability. Attenuation can only narrow
permissions, renewal authority and lifetime; it cannot restore a permission
absent from its parent or extend the parent's hard deadline. Include any parent
capability hard deadline in the effective minimum. A parent's rotating access
expiry remains a bound on each access token, not a new permanent scope deadline.

Centralize version/capability checks, issuance errors, and sanitized audit data
there as well. Unsupported issuance must fail without falling back to an owner
credential or dropping restrictions. Retry/reissue goes through the same service
with the original resolved scope bounds. Client-side lazy refresh remains the
JobDB client's job; it is not reissuance with a fresh timeout.

Test the centralized issuance contract against local attenuation. Recipe compilation, Git defaults, include limits, capability
handoff, and operation code should need no call-pattern changes for that switch.

### Isolation and lifetime

Keep the trusted controller outside the operation's isolation boundary. It
retains the owner lease, checks the policy, and issues an invocation delegation.
The operation must have no access to its owner token, admin ingress, signing
key, unrestricted local JobDB API, or broader resolver socket.

Give the isolated operation its restricted capability through a private pipe or
inherited file descriptor, using the existing import/export format. Existing
helpers may hold it on the operation's behalf, but a revocable helper is not
required for the token lifetime guarantee. Process/filesystem/network isolation
must prevent access to the controller or another invocation's credentials.
In-process Go ops remain trusted.

Every requested restricted token must have an immutable hard deadline equal to
the earliest applicable absolute deadline, with self-renewal allowed only before
that deadline:

```text
notAfter = min(active enclosing scope deadlines,
               node/task total deadline,
               task-attempt deadline,
               job/execution deadline,
               parent capability hard deadline,
               any shorter operation timeout)

usableUntil = min(current parent lease expiry, notAfter)
```

Ignore absent bounds when taking the minimum. Use the execution engine's
resolved deadlines and recorded scope-entry times, not `now + configured timeout`
at token issuance. For a five-minute task starting at 12:00, the hard deadline is
12:05; if its enclosing sequence expires at 12:03, the hard deadline is 12:03.
Preparation consuming 30 seconds does not move either deadline. A refreshed
parent lease may allow a later access expiry, but never a later `notAfter`.

Let the JobDB client refresh lazily until the fixed hard deadline. Its current
local-attenuation API owns refresh and has no author-set TTL or renewal grant. The controller alone renews the
owner. Renewal is runtime-managed transport authority, not an author-set business
permission. If the operation needs no delegated calls, issue no token.

c2j already passes node timeouts into execution scopes in
`pkg/worker/compiler/execution_scope.go`. The JobDB engine computes the
minimum of task/attempt and enclosing deadlines in `workflow/execution_task.go`.
Use the engine’s public task deadline API; do not reconstruct it by scanning
chapters, since execution records can still be buffered when a task starts. Include any shorter operation-local timeout
before minting the token, rather than discovering that timeout inside the op.

Retries may receive a new attempt deadline under existing runtime semantics,
but remain bounded by unexpired enclosing/total deadlines. Resume uses recorded
deadlines; token issuance and renewal never restart a timeout budget. If there
is no finite applicable deadline, restricted execution requires a finite
configured runtime timeout before issuing a token; do not issue an unbounded
renewable token as a fallback.

Early task completion or cancellation need not revoke an issued token. It can
remain usable until its fixed hard deadline or loss of the parent lease,
whichever occurs first. This is an accepted property of this design. Destroy
local copies and close local helpers at task exit, without claiming to revoke
copies held elsewhere. Issuing a retry's token does not revoke earlier tokens.

### JobDB hard-deadline renewal contract

Disabling renewal whenever `notAfter` is set was a bug fixed in JobDB main.
The supplied documents still describe that bug; this proposal assumes the
corrected contract rather than requesting a new feature.
Verify the fixed dependency during integration.

A delegation can refresh while `now < notAfter` and the
exact parent lease is still live. Preserve the original absolute deadline,
permission set on every refresh.
Clamp each new access expiry to the parent expiry and hard deadline. At or after
the hard deadline, reject both resource operations and renewal, including renewal
using an access-expired token. An expired access token can still refresh before
the hard deadline under the existing lazy-refresh protocol.

Delegations without a hard deadline remain valid for other clients, but c2j
always supplies one for restricted task execution. Do not drop `notAfter` to make renewal work
against an older server. Treat bounded renewal as a required server/client
capability and reject unsupported restricted execution. The JobDB guide and
renewal truth table should reflect the bug fix; c2j integration tests must cover
this behavior.

Use JobDB's delegation-only ingress for restricted token traffic. Existing
`run --with-lease` imports an owner lease for a complete workflow run; a delegated
snapshot has no execution state and cannot drive that lifecycle unchanged.
Reuse its protected capability import mechanism for an operation-specific path,
not as an unrestricted dispatcher fallback.

## Secrets, configuration, chapters, and preparation

Every route to a secret must use the same effective node policy: materialized
inputs, `c2j secret get`, the Git helper, and any task-context resolver. Existing
`scope.AllowSecrets` based on input references must become an admission check,
not an independent source of authority. Do not resolve operation inputs through
the owner lease and bypass the restriction.

Authorization precedes every cache lookup. A value fetched by a publishing node
must not become available to a later Codex node through a job-wide cache. Keep
task plaintext local; keep any shared cache exclusively in trusted code with
per-request checks and expiry/liveness enforcement. Test JobDB's workflow cache
and fresh lease-resolution paths separately. Redaction is not authorization.

JobDB delegations currently grant neither configuration reads nor artifact-byte
downloads. The controller supplies referenced configuration and supports existing
execution-interface config reads, without per-node config limits or exposing its
owner credential. It also supplies declared input artifacts. No new JobDB config
permission or artifact download permission is implied.

Keep chapter access out of the first ordinary-op authoring surface. The controller
writes task results received through the task protocol. For a specialized remote
task producer, later support symbolic task-output selectors that the controller
maps to exact ordinals, task types, and artifact names. Avoid recipe-authored
numeric ordinals, which are brittle across compilation/retry. JobDB chapter reads
also expose bodies/metadata and artifact descriptors, not just “status”; chapter
writes confer control over what downstream tasks believe. Neither should be
automatically granted for convenience.

Trusted preparation must never run downloaded extension code, workspace hooks,
or package scripts with preparation-only credentials. Move such execution into
the admitted operation environment or a separately restricted preparation scope.
Credential files and sockets must not enter snapshots or survive into a sibling
scope. A privileged follow-up node should execute pinned trusted code over
validated inputs; a node policy cannot make attacker-modified publishing code safe.

## Integration and rollout

The implementation pins JobDB main for local attenuation and corrected refresh.
It also requires the engine to expose its effective task deadline. Relevant c2j
integration points are common recipe metadata, include/shared expansion,
compiled invocation serialization, `pkg/worker/ops/taskworker.go`,
`pkg/executionruntime/git_profile.go`, and `pkg/childbroker`. The include resolver
already preserves source/commit metadata in `InlineInclusionMetadata`; extend
that trusted provenance through operation policy evaluation instead of
reconstructing source identity from the flattened recipe tree.

Persist the lexical policy and its version with the invocation; use the engine’s
effective absolute deadline at execution; never persist bearer tokens or secret values. Apply policy
validation before credential-bearing preparation. Retries cannot acquire broader
authority because configuration or recipe references changed. Revalidate against
current deployment restrictions when resuming; replay needs no new credential.

Introduce an explicit restricted execution contract/version, enforced during
worker admission. Old workers must reject jobs requiring it rather than ignore
unknown YAML. During migration, deployment policy may retain legacy execution
for existing jobs; recipes cannot select legacy mode to escape a limit. New
restricted jobs use the defaults above. Do not advertise per-node isolation
until the launcher supplies the required boundary; the existing per-job container
alone does not isolate an owner controller from arbitrary operations inside it.

Roll out by upgrading submitters and workers together. Recipes without any
`perms` retain legacy behavior; `perms: {}` opts the whole expanded recipe into
restricted defaults. A foreign include cannot opt out of an active policy.
Local attenuation changes only the issuer implementation, not the recipe API. Reject unsupported restricted execution rather than silently
using owner authority or removing the deadline.

Future inspection tooling can show requested/effective permissions, inherited
limits, source boundaries, automatic/explicit secret names, effective hard deadline, issuance method, and denial
reasons. Any future audit records should include invocation and delegation identities,
policy digests, and decisions, never credentials.

Acceptance tests should cover:

- Codex obtains its model/RO credentials but cannot directly resolve the RW
  secret by input, CLI, helper activation, direct HTTP, or cached lookup.
- Root/ordinary nodes get default RO credentials; only designated publishing
  nodes get the default RW credentials. Explicit policies and null profiles can
  remove defaults; include ceilings bound automatic grants.
- Publishing obtains only its admitted credentials; subsequent and concurrent
  nodes are not given credentials from another scope.
- Replacement, deny-all limits, includes/shared nodes, retries, and resume
  preserve lexical limits within a job; old workers reject the contract.
- Submission succeeds only when enabled, through direct and broker paths; a
  receiver can independently run with greater authority than the submitter.
- Named config references, including foreign include and manifest defaults,
  resolve without permission declarations. Empty grants/limits do not block
  config reads, including existing execution-interface reads.
- Same-repository includes inherit; cross-repository includes require explicit
  caller operation limits. Nested includes, source aliases, secret cache hits,
  deferred secret references, and preparation cannot bypass those limits.
- Recipe permission keys are nested, new system namespaces inherit independently,
  and only the central issuer translates them to JobDB's dotted wire names.
- All issuance routes use the central service; local
  attenuation obeys the permission/deadline contract, with no owner fallback.
- A five-minute task token renews within its deadline and fails at/after it;
  a shorter enclosing scope wins. Preparation, retries, and resume do not reset
  the applicable total budget, and operation-local timeouts also constrain it.
- Access-expired tokens renew before the hard deadline but never after it;
  every refreshed token preserves the original deadline and permissions.
- Parent loss invalidates tokens early; child refresh never renews the owner.
  Early task completion leaves the documented residual validity, and a new
  attempt's token cannot extend an old token's deadline.
- Inputs/results/artifacts still flow with no chapter grant, replay resolves no
  secrets, and missing isolation or delegation support fails closed.

The key distinction is between **requested authority**, **the ceiling imposed by
trusted policy**, and **the credential actually reachable by the operation**.
All three are needed for the Codex/publisher separation to hold.
