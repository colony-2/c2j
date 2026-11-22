# Choose and edit execution bases

Use `c2j execution bases` to see declared capabilities and CPU/memory request
limits. The catalog describes support, including busy hosts; it does not measure
free capacity or provision an executor. `get` uses the same readable view, with an
optional exact base filter. Add `--json` to any command for structured output.

```sh
c2j execution bases --jobdb https://jobdb.example/tenant
c2j execution bases get linux --jobdb https://jobdb.example/tenant --json
c2j execution bases check 'linux*' --capability docker --cpu 4 --memory 8Gi \
  --jobdb https://jobdb.example/tenant --json
```

Unlisted bases and capabilities are **not allowed**. Names match case-sensitively
using whole-name `*` matching. Omitted base and exactly `*` search all declared
offerings. One offering must satisfy the complete request: capabilities from one
configuration cannot be combined with limits from another. An empty catalog
allows no requests, even when a request has no explicit constraints. Explicit
`oci:` selectors and OCI declarations are unsupported.

CPU/memory columns describe explicit requests. Omission uses provider defaults
and adds no constraint. Rules are inclusive ranges, `unbounded` (any positive
value), or `not allowed` (explicit requests prohibited). A request below a minimum
is refused, not rounded up. CPU uses cores/millicores, memory uses SI/IEC units;
values must be positive and exactly representable. Packages, Python and storage
are outside this catalog's policy. Runtime allocation matching still checks actual
resources independently.

## Declare support without constructing JSON

An authorized publisher uses a provisioning context, outside job operations:

```sh
# Include --jobdb https://jobdb.example/tenant on each command, or configure it.
c2j execution bases init --valid-for 24h
c2j execution bases add linux --capability docker \
  --cpu-min 500m --cpu-max 8 --memory-min 1Gi --memory-max 32Gi
c2j execution bases add macos --cpu-not-allowed --memory-unbounded
c2j execution bases capability add linux docker.workspace-mounts
c2j execution bases capability remove linux docker
c2j execution bases capability clear linux
c2j execution bases ranges set linux --cpu-max 16 --memory-max 64Gi
c2j execution bases ranges set linux --cpu-max none
c2j execution bases ranges set linux --cpu-unbounded
c2j execution bases renew --valid-for 24h
c2j execution bases remove macos
```

`init` requires positive validity and refuses to overwrite an existing catalog.
`add` requires a CPU declaration and a memory declaration. Repeat `--capability`
to declare a set; omission declares no capabilities. Range edits preserve
unspecified endpoints. `none` clears an endpoint; clearing the last endpoint
makes a resource unbounded. State switches clear bounds. Conflicting state/bound
options and inverted ranges fail without writing.

`--offer NAME` selects a configuration, defaulting to `default`. For example,
`add linux --offer kvm --capability virtualization.kvm --cpu-unbounded
--memory-unbounded` declares another complete configuration. Capability/range
edits require that offering to exist. `remove linux --offer kvm` removes only it;
`remove linux` removes every offering of that base.

Scope, generation and publication time are generated. Ordinary edits preserve
expiry; only `renew --valid-for` extends validity. Administrators can inspect an
expired structural record (`status: expired`, nonzero exit) and renew it without
changing offerings. Malformed records require a complete replacement. No-op edits
preserve revision, generation and timestamps.

## JSON and publication

Inspection emits `{status, scope, revision?, reason?, catalog?, filtered?}`.
`bases` and `get` share that envelope. `get BASE` marks its projection
`filtered: true` and `catalog.complete: false`; it cannot be imported as a full
catalog. Unknown bases return `not_found`. Check output includes a normalized
request, decision, satisfying offering identities, and offering-specific reasons
for every mismatch. Decisions are `allowed`, `not_allowed`, `invalid_request`,
`not_configured`, and `unavailable`.

Inspection succeeds for authoritative or optionally unconfigured views. Checks
succeed only when allowed; other decisions exit nonzero. Successful edits and
no-ops exit zero. JSON commands emit one object on stdout, with errors on stderr;
flag-parser errors may leave stdout empty. Mutation receipts include `changed`,
scope, revision, generation, expiry and changes.

Advanced publishers can use `set --file PATH` (`-` reads bounded stdin) to import
one complete catalog object. Export the `catalog` property of an unfiltered JSON
view, rather than the view envelope. The sole current format is:

```json
{
  "version": 1,
  "scope": {"runtime": "https://jobdb.example", "tenant": "tenant"},
  "complete": true,
  "offerings": [{
    "base": "linux", "name": "default", "capabilities": ["docker"],
    "resources": {
      "cpu": {"state": "range", "min": "500m", "max": "8"},
      "memory": {"state": "range", "min": "1Gi", "max": "32Gi"}
    }
  }],
  "generation": "example",
  "published_at": "2026-11-22T06:00:00Z",
  "expires_at": "2026-11-23T06:00:00Z"
}
```

Use a currently valid UTC interval and the exact resolved runtime/tenant scope.
Embedded JobDB uses `embed:///` and tenant `0`. Every offering needs explicit
non-null capabilities, resources, CPU/memory rules and resource states. A range
needs at least one non-null endpoint; other states forbid endpoints. Names and
capabilities are sorted canonically. Duplicate members/identities/capabilities,
case aliases of JSON fields, unknown fields, invalid quantities, trailing JSON,
invalid UTF-8, and input/canonical content over 64 KiB are rejected. This replaces
the unused names/dynamic catalog format directly; no old-format reader is provided.

The reserved tenant key remains `_c2j_execution_bases`. Each mutation reads one
snapshot, changes only selected properties and conditionally writes only that key
against the same revision. Unrelated configuration is preserved. Conflicts retain
the winner and return without retrying. A lost response triggers exact-content
readback, never another write. Uncertainty retains the exact candidate in the
administration diagnostic; compare it before retrying. Changed imports generate
fresh generation/publication metadata; metadata-only differences are no-ops.

Inside operations, discovery/get/check use the authenticated read-only execution
helper. Target overrides are rejected. Mutations refuse operation contexts before
opening files or discovering administration credentials. A missing/expired helper
does not fall back to ambient administrator access. Generic scoped configuration
reads continue to deny the reserved key. Capability declarations do not grant
permissions.

## Admission boundaries

Publish accurate offerings, then enable `C2J_REQUIRE_EXECUTION_BASE_CATALOG=true`
in standalone drivers to require catalog admission. Missing/false disables catalog
reads for admission; explicit OCI selectors still fail. Invalid boolean settings
fail option validation. A missing, malformed, expired or unreadable required
catalog cannot authorize work and is never treated as an empty denylist.

Library drivers set `executionruntime.Runtime.BaseCatalog` to an
`executioncatalog.Policy` with `Required: true` and trusted scope. The runtime
supplies a fresh current-owner-lease reader. Keep task workers wrapped with
`WrapTaskWorker` and use `Stage` as the compiler staging callback. Submission
drivers supply an authorized reader through `starter.StartRecipeJobOptions.BaseCatalog`,
`recipejob.BuildStartJobRequest.BaseCatalog`, or `executioncatalog.WithPolicy`.
Preparation and descriptor construction remain pure; final submission/restart/
append recheck current authority. Operation environment variables cannot weaken
the parent's policy.

Each required check reads fresh authority once, bounded by ten seconds and any
earlier caller deadline. Known initial requirements are checked before submission.
Repository defaults are combined and checked before dependency installation;
exit-75 requirements before staging/handoff; actual reschedule payloads before
publication or lease release. Refused candidates restore prior staged demand.
Completed task results replay without checking their historical requirements.

`UNSUPPORTED_EXECUTION_BASE` means no matching base (or an explicit OCI selector).
`UNSUPPORTED_EXECUTION_REQUIREMENTS` means matching bases exist but no complete
offering accepts the request. Both are nonretryable application refusals with
source, scope, revision, generation and reasons. Required-authority failures use
`EXECUTION_BASE_CATALOG_UNAVAILABLE`, an infrastructure error. Lease loss and
cancellation retain their identities.

Catalog edits do not retract already published demand or restart stranded jobs.
Correct the owner's request and restart at a boundary that reloads it. These
commands neither install tools nor establish provider availability.
