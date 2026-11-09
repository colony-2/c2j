# Retained input history

`input.NewHistoryService(input.HistoryConfig{Reader: control})` constructs the
additive `input.HistoryService`. A `*workflow.SWFWorkflowControl` supplies the
read-only interface; no operation registry, worker or replay service is needed.
See [compiling examples](history_example_test.go) for list, detail, artifacts,
presence-aware JSON and typed error handling. Configure the control with your
existing JobDB engine and tenant authorization. Keep one history service alive
across requests.

The public methods and DTOs are in [history.go](history.go). `errors.Is` recognizes
`ErrHistoryEntryNotFound`, `ErrHistoryArtifactNotFound`,
`ErrHistoryInvalidCursor`, `ErrHistorySnapshotExpired`, `ErrHistoryUnsupported`
and existing `ErrArtifactUnavailable`. `errors.As` exposes `HistoryError`
coordinates/capability. Storage and authorization errors retain their causes;
they never become successful empty pages. Missing retained bytes can fail on
open or read, without removing the manifest association.

First-page reads capture all retained execution records into an immutable
snapshot, ordered by occurrence ordinal and full identity. Page size is 1–500
(default 50). Snapshot cache defaults are five minutes and 128 snapshots. These
limits bound snapshot lifetime/count, not job occurrence count. Eviction, TTL
expiry and a different service instance explicitly expire tokens. Use sticky
routing for pagination, or restart pagination after expiry. Tokens bind tenant,
job, version, revision and last entry. They confer no authorization. Snapshot
content remains usable while cached even if backend retention later changes;
artifact bytes remain an on-demand storage read.

Occurrence IDs anchor full successful generator invocation and result ordinal;
retained collection invocation is the fallback. Collection retries share a turn;
a separate wait reusing a form receives a discriminator. Pairing requires the
same enclosing invocation and frozen form. Comparison accounts for known zero
fields expanded by the existing transport; raw generator JSON is preserved.
Ambiguous candidates remain separate diagnostic occurrences. Title, question,
request and schemas are recorded values. No universal summary is persisted by
current producers, so summary presence is false. Legacy gaps remain explicit.

`RawForm`, `FormFields` and accepted output use `json.RawMessage`; absence is
separate from explicit null and numbers never pass through float64 in this
reader. Outputs are saved normalized accepted outcomes, including autofill and
alternate completion, rather than original HTTP submissions. Actor receipts
remain recorded receipts. Actor provenance is `submission_receipt` for external
collection, `automation_receipt` for generated review/structured autofill and
alternate/fallback receipts, and `reported_receipt` for ordinary autofill's
caller-supplied receipt. The latter can contain caller-chosen human actor/time
fields and must not be presented as an authenticated submission. Missing receipts
remain `unavailable`, or `reported_legacy` when a legacy user ID was recorded.
Classify by this provenance and response source, never the receipt's actor kind
alone. No recorded receipt/output fields are removed or rewritten. State
never infers termination from job failure, elapsed fallback time or absence
from a queue. Unknown is intentional when occurrence termination is unrecorded.

Request manifests use documents, refs in structured request, explicit invocation
`artifacts`/`artifact_keys`, direct generator-produced output attachments, and
unsupported context path/glob/output placeholders.
Response manifests use refs in committed accepted output plus direct attachments
produced at that result ordinal. Inherited response wrapper bindings, private
input/checkpoint envelopes and Git payloads are excluded. Repeated associations
share a byte identity. Attached child refs resolve only through recorded
containing `Outputs`/`Artifacts` maps; missing maps remain unavailable. External
refs never cause a fetch. Stored refs open by tenant and original producer job,
ordinal and name after occurrence membership validation. A retained ref does
not guarantee that storage still retains its bytes.

Detail actionability is separately observed live. Only guarded review/structured
forms matching the authoritative collection occurrence, completion ordinal and
frozen request identity can be actionable. Submission must still use existing
exact-task guarded APIs and recheck for conflicts. Ordinary history always
remains read-only. Optional `MapNode` must be a read-only mapping; its failure
leaves retained history readable.

Measured compatibility: Go 1.26.1 linux/arm64, JobDB
`v0.0.78-0.20261108193348-5dee36022e67`, isolated file-backed SQLite runtime.
Integration tests close/reopen retained waits, read archived/private inputs and
original producer bytes, and interleave acceptance/new generation with paging.
Older JobDB versions, shared deployment retention and Cortex HTTP/browser
integration require separate validation. This feature changes no schema or
existing `story.Service`, current form, document or submission contracts.

Each list/detail locator now includes `Restart`, a
`HistoryRestartAssociation`: `ScopeID`, typed `Relationship` (`exact`,
`containing`, or supplied story `body`), and `UnavailableReason`. This is
independent of live actionability. A containing scope reruns enclosing work;
an association grants no authorization and does not guarantee restart success.

By default, `RecordedHistoryRestart` performs a pure lookup over the same
retained execution snapshot. It matches full invocation identity and the saved
boundary, then selects the nearest recorded restartable scope. Compact logical
records sharing an ordinal remain distinct. Collection-only occurrences resolve
through their recorded task identity and first wait/result boundary, rather than
the compatibility synthetic occurrence ID. Retry and job-attempt components are
kept intact. Copied prefixes use scopes present in the selected job; a source-job
pointer alone cannot establish a target. Absent, ambiguous or unsupported
boundaries produce an unavailable reason. No worker, registry, story replay or
recipe operation runs during this lookup.

If an application already has an immutable story mapping, it may supply
`HistoryConfig.MapRestart`. Match the complete locator, including invocation,
attempts and collection coordinates, within the supplied selected `JobKey`.
Return a typed association or an error. The reader checks that the returned
scope is uniquely recorded in this snapshot and matches invocation lineage;
mapping errors leave the saved form and document manifests readable. Do not
call story assembly/replay from this callback. Existing `MapNode` remains
supported independently. Associations are captured with the immutable page
snapshot and may expire with it.

The compiling `restartSavedInput` example passes the page's tenant/job and the
selected entry's `Restart.ScopeID` to `story.Service.RestartRecipeJob` as a
separate explicit action. Leave `ChapterOrdinal` unset: the existing API rejects
conflicting selectors. Use a fresh request ID for the intended action, handle
restart errors, and navigate to the returned new job identity. Keep the original
history readable. This adds no answer editing or restart engine.
