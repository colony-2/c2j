# Job labels

A label is a `key=value` pair with a color on one job. It uses JobDB extended
metadata under `c2j.label.<key>`, for example `{"value":"foe","color":"blue"}`.
This namespace is a convention, not an authorization boundary. A raw client
with JobDB write authority can write the same keys.

## Operator commands

```sh
c2j job labels list JOB --json
c2j job labels get JOB friend
c2j job labels set JOB friend foe --color blue --if-version 0
c2j job labels remove JOB friend --if-version 1
```

The commands use the ordinary `--jobdb` / `--embed` target. They do not open
an operator runtime inside a recognized task invocation. Colors are `gray`,
`red`, `orange`, `yellow`, `green`, `blue`, or `purple`. Keys match ASCII
`^[a-z][a-z0-9_-]{0,62}$`. Values are 1–256 UTF-8 bytes, not all whitespace,
and cannot contain control characters, U+2028, or U+2029. Accepted text is
preserved exactly.

Pass an explicit expected version as a nonnegative decimal integer with no sign
or leading zeroes. JSON encodes versions as strings. Create a never-written
key at `0`. Removing a present key advances its version and leaves a tombstone.
Recreate using the tombstone's version, not `0`. Setting the identical canonical
label or removing a currently absent or tombstoned key at its current version
returns a receipt without advancing the version.

The Go package `pkg/joblabel` exposes `ListJobLabels`, `GetJobLabel`,
`SetJobLabel`, and `RemoveJobLabel`. Reads use the existing projection; edits
make a single compare-and-set and return the producer's receipt. A conflict
requires a new decision based on the current state. After a transport failure,
do not assume success or retry with a refreshed version: read the state first.
Known denial, lease loss, not-found, and unsupported remain distinct. A missing
optional edit port is unsupported; a masked or ambiguous 404 stays not-found.

| Keyed status | Meaning |
| --- | --- |
| `present` | Valid label and its version. |
| `malformed` | Entry version, but no raw payload. Use that version to repair or remove a malformed value. An invalid key suffix requires an authorized raw client. |
| `tombstone` | Removed label and the version to use to recreate it. |
| `absent` | Missing key in a metadata object explicitly attested complete for this read; version `0`. |
| `unknown` | The key was not observed and the metadata object was missing or not attested complete; no version. |

The compact list, recipe get/list/children, and job inspection JSON expose
`labels` (valid present labels, sorted by key) and `labels_projection`
(`projected` or `unknown`). `projected` does not imply completeness. Only
`labels_complete: true` confirms absence for keys not listed in `labels` or
`label_states`. `label_states` carries observed tombstone and malformed states
without exposing raw metadata values. Observed versions remain usable even
without `labels_complete`; completeness grants no write authority. Ordinary CLI
text is unchanged. The dedicated commands show the projection or keyed status
in text and JSON.

## Recipe and extension automation

Automation is off by default. A trusted host can opt in for one resolved job
and its exact, slash-separated node path, including the recipe ID:

```sh
c2j run one --job-id JOB --allow-job-label-node release/mark
c2j run one --job-id JOB --allow-job-label-extension 'release/mark=git+https://example.test/ops.git//mark@FULL_COMMIT'
```

Use one flag or the equivalent trusted `executionruntime.Runtime.ConfigureJobLabelEdits`
startup policy. Replace `FULL_COMMIT` with the full lowercase Git commit hash.
Recipe permissions cannot grant label edits. Explicit scoped policies or
foreign input scopes veto the host grant, and the operation requires the active
task's current ordinary lease. Delegated, canceled, expired, and superseded
leases cannot edit. There is no operator fallback.

```yaml
- id: mark
  op: job_label_edit
  inputs:
    action: set
    key: friend
    value: foe
    color: blue
    expected_version: "0"
```

`action` is `set` or `remove`. `value` and `color` are required for `set` and
must be omitted for `remove`. The op returns the same keyed state as the Go API.

An extension grant only applies to the launched Git operation whose resolved
repository, op path, and full commit match the pin. Local and Nix selectors,
unverifiable sources, and caller launch environment overrides receive no grant.
For label authorization, prepared tool environments that alter executable
selection or add launch variables are also treated as unverifiable.
Read broker settings via `jobcontext.ChildJobBrokerFromEnv` and call
`childbroker.JobClient.EditCurrentJobLabel(ctx, edit)`. The authenticated
`POST /v1/child-jobs/labels/edit` endpoint accepts the same fields as the recipe
op, not a target job or tenant. The launch token is protected, expires with the
process or task, and conveys no JobDB writer credential. Do not log or forward it.

Edits persist even if a task later fails. History replay does not edit; a real
task retry uses the original expected version and can therefore conflict.
