# Discover and refuse unsupported execution bases

A base selector can be syntactically valid yet name an environment nobody offers.
An upgraded c2j submitter or worker can check a deployment's declared offering
before publishing that demand. A refused request includes supported choices and
uses ordinary recipe failure handling. Pulse placement is unchanged.

Use `c2j execution bases --json` before selecting a name. Inside an operation this
uses its read-only execution helper; outside a job it reads the selected JobDB
tenant. Human output explicitly describes support, not free capacity. Busy,
warming and temporarily unreachable hosts still belong in the offering.

Names retain case-sensitive, whole-name `*` matching. c2j does not infer versions,
create aliases or substitute another base. Omitted base and exactly `*` remain
unconstrained. Offered names do not certify an OS patch release or installed tools.

## Publish the offering

One authorized deployment publisher declares the complete tenant-wide offering
across all eligible providers/controllers. Publish through a provisioning context:

```sh
c2j execution bases set --jobdb https://jobdb.example/tenant --file bases.json
c2j execution bases get --jobdb https://jobdb.example/tenant
c2j execution bases --jobdb https://jobdb.example/tenant --json
```

`set` accepts JSON (or `--file -` for stdin). Example only; obtain actual names and
prerequisites from the deployment owner:

```json
{
  "version": 1,
  "scope": {"runtime": "https://jobdb.example", "tenant": "tenant"},
  "complete": true,
  "named_bases": ["macos-15-arm64", "macos-26-arm64"],
  "dynamic": [{"kind": "oci"}],
  "generation": "offering-2026-11-20-1",
  "published_at": "2026-11-20T00:00:00Z",
  "expires_at": "2026-11-21T00:00:00Z"
}
```

Use the runtime URL and tenant from the resolved JobDB target. Embedded JobDB uses
runtime `embed:///` and tenant `0`. Scope must match exactly. Declare explicit
arrays, including `[]` for no named bases or no dynamic namespaces. `complete`
must be true. Names must be unique and concrete; dynamic version-one rules accept
only `oci`. Unknown fields, duplicate JSON members, duplicate rules, unknown
versions and invalid authority are rejected. Validity uses UTC and requires
`published_at <= now < expires_at`. The encoded record is limited to 64 KiB.

An OCI rule admits a concrete `oci:...` request to the provider's normal image and
platform validation. It does not promise every registry image exists. A wildcard
with no named match that intersects the open OCI namespace returns
`CANNOT_VERIFY_EXECUTION_BASE`; refine it to a concrete base. A disjoint selector
returns `UNSUPPORTED_EXECUTION_BASE`.

The reserved JobDB tenant record is `_c2j_execution_bases`. Generic scoped config
reads deny that exact key, including direct socket requests. Use typed discovery.
This separates interfaces; authorized JobDB administrators can still inspect raw
configuration. Management commands refuse operation contexts before discovering
administration credentials, and operations receive no write capability.

Publication replaces only this key, conditional on the observed configuration
revision. Unrelated configuration remains intact. A concurrent winner is retained
and the losing publisher must reread. A lost response triggers a fresh exact-content
readback, never a second write. If uncertain, retain the original generation and
payload until you have resolved whether that write committed. A changed record
requires a new generation.

## Enable admission

Upgrade all participating submitters and workers, publish the complete offering,
then set `C2J_REQUIRE_EXECUTION_BASE_CATALOG=true` in standalone c2j drivers. The
policy is independent of the catalog record: deleting or expiring the record
cannot disable required admission. An absent/false variable retains existing
unconfigured behavior; malformed booleans fail option validation.

Library drivers can set `executionruntime.Runtime.BaseCatalog` to an
`executioncatalog.Policy`, with `Required: true` and a trusted scope. The runtime
supplies a fresh current-owner-lease reader; do not give child leases configuration
grants. Acquired and supplied leases use this same policy. All task workers must
still use `WrapTaskWorker`, and the compiler must use `Stage` as its execution
staging callback.

Submission drivers supply `Policy.Read` using their authorized target's fresh
configuration reader. Use `starter.StartRecipeJobOptions.BaseCatalog`,
`recipejob.BuildStartJobRequest.BaseCatalog`, or
`executioncatalog.WithPolicy(ctx, policy)` for descriptor and lower-level c2j
submission adapters. Preparation and descriptor construction remain pure. Keep
policy attached through final publication, including restart and append callbacks.
Zero-policy constructors/callers retain existing behavior. A required policy
without a reader fails closed. Operation subprocess environment overrides cannot
lower the parent broker's policy or switch it to ambient admin credentials.

Admission reads once per check, with a ten-second limit capped by caller deadlines.
It does not cache authority between tasks, retry internally, poll or reschedule a
refusal. It checks known initial demand before new submission; unresolved future
requirements are checked when their real unfinished task executes. Repository
defaults are combined and checked before dependency installation. Exit-75 combined
requirements are checked before staging or yielding. Changed reschedule payloads
are checked before their conditional write. Completed task results replay without
revalidating their historical environments.

`UNSUPPORTED_EXECUTION_BASE` is an application failure with selector, real boundary
source, sorted choices, dynamic rules, trusted scope, revision and generation. Its
recipe failure is nonretryable. Existing bounded recipe recovery still applies;
retrying an unchanged request cannot publish it. `CANNOT_VERIFY_EXECUTION_BASE` is
a nonretryable system failure requiring a more precise request. Missing, expired,
malformed or unreadable required authority returns
`EXECUTION_BASE_CATALOG_UNAVAILABLE`, an infrastructure failure. Lease loss and
cancellation retain their ownership/cancellation meaning.

## Correct existing waits

Catalog publication does not drain an already-stranded job or retract demand
withdrawn after a successful check. Correct the owner's saved recipe/hook and
restart at a boundary that reloads it, or submit a corrected job while preserving
old evidence. Retrying the same pinned task cannot load an edited hook from main.
Do not invent a task failure for a task no upgraded worker has reached.

For job `3MJfwiz2fzcZLbInFqDakTKw3PK`, the retained request is
`macos-sequoia-15.7.7-arm64`. Confirm the actual offered Apple Silicon host and
signing prerequisites before selecting its replacement. Keep Linux/fixture tests
and the mandatory real macOS signing check. This change neither mutates that job
nor installs signing tools.

Raw JobDB clients and old c2j binaries can still publish unchecked demand. Confirm
that provider-launched c2j workers can upgrade independently of Pulse before
requiring admission. Register additions before use, renew authority before expiry,
and drain withdrawals where possible. Local SQLite/HTTP tests establish c2j
behavior; they do not establish deployment inventory, rollout or real Mac success.
