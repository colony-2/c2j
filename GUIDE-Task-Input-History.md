# Read retained input occurrences and member files

Use these commands inside a live c2j task to read the exact frozen forms,
accepted outputs and associated files retained by a job:

```sh
c2j job inputs list self --json --limit 50
c2j job inputs list JOB --json --cursor NEXT_PAGE_TOKEN
c2j job inputs show JOB --entry ENTRY_ID --snapshot SNAPSHOT_TOKEN --json
c2j job inputs artifact JOB --entry ENTRY_ID --member ARTIFACT_ID --snapshot SNAPSHOT_TOKEN --output ./new-file.bin
```

`self` is resolved by the authenticated helper. The caller cannot select a tenant,
credential, producer key or role. These commands require the helper even outside
a recognized task environment. `--jobdb` and `--embed` overrides are rejected;
ambient JobDB credentials and general artifact inventory are never fallback routes.
The normal runner and runtime-backed recipe-test worker sessions install the same
adapter. Isolated recipe cases without a worker runtime do not supply this capability.

List emits one native page, default 50 entries, range 1–500. Copy its
`next_page_token` to `--cursor`; copy its `snapshot_token` to detail/download
commands. Select `entry_id` from that page and `artifact_id` from the selected
entry's request or response manifest. Request IDs, task ordinals, basenames,
paths, URLs and raw storage keys do not identify a member.

The helper retains one existing `input.HistoryService` for its live authority
across CLI processes. Snapshots freeze a retained prefix while responses and new
occurrences advance. They default to five minutes and 128 observations. Expiry,
eviction and helper replacement require fresh discovery. Tokens are bound to the
helper instance, tenant and selected job. They grant no read authority and must
not be stored as durable consumer state.

Every operation freshly checks task/scope/lease lifetime and selected-job backend
permission, including cache hits. Member opening first checks exact occurrence
membership, then reads the retained producer's original job, ordinal and name
through context-aware authorized runtime access. Producer permission is checked
again on open. Cancellation, lease replacement, failed renewal, expiry and helper
shutdown interrupt blocked reads. Same-authority renewal extends lifetime without
replacing the snapshot cache.

## Native metadata and errors

List/show write one JSON success envelope to stdout:

```json
{"version":1,"page":{"job_id":"...","snapshot_token":"...","entries":[],"completeness":"complete"}}
```

```json
{"version":1,"entry":{"entry_id":"...","snapshot_token":"...","completeness":"partial"}}
```

The payload is the native DTO from [the history API](pkg/input/history.go), not
an alternate projection. It preserves frozen `raw_form`, `raw_accepted_output`,
absent/null values, exact retained JSON numbers, both manifests, association
pointers, original and resolved refs, provenance, locators, diagnostics and
completeness. A job without retained execution coverage is partial, even if its
entries are empty. Genuine historical gaps stay explicit. Operational failures
while restoring private inputs cannot become successful partial metadata.

Failures exit nonzero, emit no success payload, and write one diagnostic to stderr:

```json
{"version":1,"code":"nonmember","detail":"retained input history operation failed","job_id":"...","entry_id":"...","artifact_id":"..."}
```

| Code | Meaning |
| --- | --- |
| `unsupported` | Absent capability/helper or unsupported version; upgrade CLI and worker together. |
| `invalid_request` | Invalid flags, fields, selectors or limit. |
| `invalid_cursor` | Malformed, tampered or incorrectly scoped native token. |
| `snapshot_expired` | Expired, evicted or different-helper observation. Rediscover explicitly. |
| `entry_not_found`, `nonmember` | Missing occurrence or member outside that occurrence. |
| `unavailable` | Associated bytes genuinely unavailable; association remains retained. |
| `denied` | Helper/backend authorization denied, including producer denial. |
| `canceled`, `timeout`, `lease_lost` | Request/task/scope cancellation, deadline or lost execution authority. |
| `size_mismatch`, `read`, `close`, `write`, `storage` | Integrity, I/O or backend failure. |
| `protocol`, `transport` | Malformed/incomplete responses or connection failure. |

Diagnostics include applicable coordinates and backend code/status when the
backend exposes them. Go errors retain `errors.Is`/`errors.As` causes; authority
causes take precedence over unavailable/storage wrappers. Metadata clients must
validate version, expected page/entry payload and complete JSON, not just exit
status. Metadata travels as raw JSON without floating-point conversion.

## Complete bytes and atomic output

Artifacts require a new file path; `--output -` is unsupported. Existing files,
symlinks and concurrently created destinations are preserved. The CLI stages in a
private 0600 file in the destination directory, verifies all reads and closes,
and commits using an atomic no-replace hard link. Filesystems without that
operation fail rather than overwrite. Precommit failures remove the temporary
file and leave no destination. Successful downloads emit no file bytes to stdout.

The adapter checks the full nonnegative retained size, runtime descriptor name,
any known runtime size, all bytes through EOF, source close and live authority.
Unknown runtime size does not disable retained-size checks. Private frozen-input
restoration uses the same verifier. Binary, empty and split UTF-8 data remain bytes.

The helper uses download version 1, `application/octet-stream` and exactly one
`X-C2J-Download-Size` header containing the nonnegative retained member size in
decimal bytes. The client requires ordinary HTTP EOF and that exact byte count.
There is no additional successful-completion marker. Missing/invalid size,
short/excess bytes, incomplete HTTP framing and any returned read/close error
prevent file publication. Failures observed by the helper use a base64url-encoded
versioned diagnostic in `X-C2J-Download-Error`; malformed error trailers also fail.
The helper checks selected-job/lifetime authority again after its source closes.
Existing saved-diff transport/errors are unchanged.

The JobDB remote client buffers immutable artifact bytes. If it returns the full
correct bytes successfully, the bridge accepts them without an additional
upstream success signal. JobDB can hide original source read/cleanup errors after
that byte count; the bridge does not promise to recover those unreported errors.
Errors actually returned by the runtime, the helper transport or local file I/O
still fail the operation. Retained-size checks establish byte count, not a new
cryptographic content proof.

## Compatibility and publication acceptance

Use a CLI and worker built from the same supporting c2j publication, with metadata
and download protocol version 1. Public Go `HistoryService` methods and native
cache defaults remain compatible.

The c2j client and embedded runtime retain JobDB
`v0.0.94-0.20261119021921-f885723cc03c`. Local SQLite reads and the real HTTP
client are exercised on Go 1.26.1 Linux arm64. Remote retained-history support
requires a server preserving the existing typed HTTP 403 permission envelope.
The tested server is published JobDB
`v0.0.99-0.20261121210721-67cce3c7a795`, repository
`https://github.com/colony-2/jobdb.git`, commit
`67cce3c7a7954cc334b06f5d881a2b16d3f9c490`, ref `refs/heads/main` at acceptance.
The existing old client recognizes that server's bare/wrapped denial responses;
no c2j module replacement or runtime dependency upgrade is needed for this route.

Acceptance builds that exact published server in a separate disposable process,
checks its linked dependency identity, creates retained records through the c2j
client, and runs the actual CLI/helper against it. Tests cover native metadata,
advancing snapshots, exact binary/UTF-8 bytes, cached-operation revocation,
original-producer denial, authorization restoration and failed-stream atomicity.
The old pinned server still rejects revoked access but reports `storage` instead
of `denied`; an explicit negative compatibility control retains this limitation.
Old servers do not satisfy the supported remote diagnostic contract. There is no
credential substitution or alternate read route when they fail.

This establishes retained-read interoperability, not a general c2j runtime/goal
upgrade certification. Installing the newer JobDB module into c2j would change
existing goal semantics and is not part of this bridge. The newer server also
requires manual conversion of existing SQLite formats 11/12 to format 13; see
its [migration instructions](https://github.com/colony-2/jobdb/blob/67cce3c7a7954cc334b06f5d881a2b16d3f9c490/docs/MIGRATION-IMMUTABLE-GOALS.md).
Old-storage reopening compatibility failed in the owner's assessment. Fresh
format-13 test databases do not prove an existing deployment can be upgraded
without conversion. No database conversion or deployment was performed here.
The human explicitly rejected maintenance branches; no maintenance publication
or branch-initialization prerequisite is introduced.

The human cancelled the extra successful-completion-marker prerequisite. Complete
immutable bytes use retained-size checks and ordinary HTTP completion; no new
JobDB source-cleanup protocol is required. That scope correction does not waive
typed authorization diagnostics or existing c2j compatibility checks.

Old/absent helpers fail explicitly. An old CLI can print generic job help and
exit zero for the unknown `job inputs` route; upgrading only the worker cannot
repair that binary. Consumers must require a valid versioned native payload.
The exact delivered commit is established by the adaptive3 publication receipt
and independent ref resolution, not the pre-merge workspace baseline.

After publication, download the actual merge output to scratch. From a fresh
clean checkout of its commit, set these inputs from that receipt and route:

```sh
export C2J_INPUT_HISTORY_PUBLISHED_REPOSITORY=https://github.com/colony-2/c2j.git
export C2J_INPUT_HISTORY_PUBLISHED_REF=refs/heads/main
export C2J_INPUT_HISTORY_PUBLISHED_COMMIT=ACTUAL_RECEIPT_COMMIT
export C2J_INPUT_HISTORY_PUBLICATION_RECEIPT=/scratch/actual-merge-output.json
go test -mod=readonly -count=1 -tags=postpublication -timeout=10m ./cmd/c2j -run '^TestInputHistoryPublication'
```

This check requires a published/already-present receipt, resolves the ref to that
exact commit, and builds the CLI there. Its companion helper comes from the same
published source. It reruns retained metadata/byte, revocation and failed-stream
acceptance through both supported transports. Missing inputs or mismatched refs
fail; these checks are not pre-merge skips or a publication claim.

Membership, inherited attachments and caller-reported roles do not prove agent
authorship or an earlier presentation. Recipes retains F03, exact presentation/
predecessor checks and successful agent-invocation output validation after pin
adoption. Production declarations/derivatives remain disabled. This bridge adds
no answering, correction routing, Manager recovery, Cortex/Critrev implementation
or approval authority, and publication does not wait for Cortex adoption.
