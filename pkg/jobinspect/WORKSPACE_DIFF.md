# Saved cumulative workspace diffs

The original human instruction supplied by Cortex is preserved verbatim:

> the cli command should allow you to get the diff at any step/scope/thingy, not just last. same with api. always same pattern (latest workspace diff to that point).

`job diff JOB` copies the original retained cumulative patch, from that workspace's active base to its persisted head. It executes no Git and reconstructs no patch. Missing boundary, lineage or patch evidence produces `unavailable`, without substituting another state. `job diff JOB --json` reads metadata only. `job changes` is a deprecated metadata alias.

```sh
c2j job log JOB
c2j job story JOB
c2j job diff JOB
c2j job diff JOB --workspace main --output candidate.diff
c2j job diff JOB --task 'EXACT_NATIVE_TASK_ID' --attempt 1 --boundary before
c2j job diff JOB --scope 'EXACT_NATIVE_SCOPE_ID' --boundary after --json
```

Copy native IDs from log/story; names and numeric chapter ordinals are not selectors. Task and scope are mutually exclusive. An explicit attempt is positive and requires a point. Its omission chooses the latest recorded attempt, including waiting or failed attempts. The workspace selector accepts an exact workspace scope ID or reserved `main`; cell aliases are not workspace IDs. Omitted workspace without a point means main. At a selected task or scope, omission uses its recorded active workspace; inherited overrides stay in that workspace. Explicit `main` continues to select the owning workspace. The boundary defaults to after; an explicit boundary requires a point.

`--json` conflicts with every explicit `--output`, including `-`. The metadata alias permits a new output file. Output files are created exclusively, and failures remove incomplete new files. Unavailable metadata is emitted as JSON with nonzero exit. Diagnostics go to stderr. The removed CLI history/comparison flags (`--all`, `--commit`, `--from`, `--through`, `--base`, `--selection`, `--cursor`, `--limit`) return migration guidance. Existing public Go history capabilities remain available.

## Go capability

`WorkspaceDiffAPI` is separate from the mandatory inspection interface:

```go
type WorkspaceDiffAPI interface {
    OpenWorkspaceDiff(context.Context, WorkspaceDiffRequest) (*WorkspaceDiff, io.ReadCloser, error)
}
```

Both `*jobinspect.Service` and `childbroker.JobClient` implement it. For an existing authenticated runtime:

```go
service := &jobinspect.Service{
    Runtime: runtime, Tenant: tenant,
    Authorize: authorizeJob, // apply the application's inspection policy
}
var api jobinspect.WorkspaceDiffAPI = service
```

Inside a task, keep the authenticated helper route:

```go
broker, present, err := jobcontext.ChildJobBrokerFromEnv(os.Getenv)
if err != nil { return err }
if !present { return errors.New("task helper required") }
var api jobinspect.WorkspaceDiffAPI = childbroker.JobClient{Broker: broker}
```

An older helper fails with upgrade guidance. This capability never falls back to ambient credentials.

```go
func copySaved(ctx context.Context, api jobinspect.WorkspaceDiffAPI,
    request jobinspect.WorkspaceDiffRequest, dst io.Writer,
) (*jobinspect.WorkspaceDiff, error) {
    metadata, body, err := api.OpenWorkspaceDiff(ctx, request)
    if err != nil { return nil, err }
    if metadata.Status == "unavailable" {
        return metadata, fmt.Errorf("saved comparison unavailable: %v", metadata.Reasons)
    }
    _, copyErr := io.Copy(dst, body) // read through EOF, even if Bytes is known
    return metadata, errors.Join(copyErr, body.Close())
}
```

Use `WorkspaceDiffRequest{JobID: jobID}` for latest main. For a selected point:

```go
boundary, workspace, attempt := "before", "main", 1
request := jobinspect.WorkspaceDiffRequest{
    JobID: jobID,
    WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{
        Task: &nativeTaskID, Attempt: &attempt,
        Boundary: &boundary, Workspace: &workspace,
    },
}
```

Pointer options preserve omitted versus explicit values. Blank/null options, duplicates, nonpositive attempts and conflicting selectors are rejected. The compiled, executed examples and consumer lifecycle tests are in `workspace_diff_example_test.go`.

## Metadata and JSON

The authenticated metadata operation uses the existing inspection envelope:

```json
{"operation":"workspace_diff","job_id":"JOB","workspace_diff":{}}
```

A selected scope uses the same pattern:

```json
{"operation":"workspace_diff","job_id":"JOB","workspace_diff":{"scope":"EXACT_NATIVE_SCOPE_ID","attempt":1,"workspace":"main","boundary":"before"}}
```

The standalone request omits `operation` and flattens the selector options next to `job_id`. Generic inspection selector fields are not reinterpreted for this operation.

`Response.WorkspaceDiff` contains version 1 metadata. `Requested` preserves omissions. `Resolved` supplies point, attempt, execution status, boundary and workspace ID. `Observation` and `Cutoff` are opaque identifiers of durable evidence; they are not history cursors or reserved snapshots. A later read can have a different observation while the selected endpoints/source stay the same.

`Workspace` records repository, cell, scope ID, base ref, base commit, head commit and explicit empty-base state. `StateProducer` identifies the recorded boundary observer/producer. `Source.Producer` and `Source.Key` identify the original saved patch, including through unchanged-state reuse and authorized foreign incoming references. They can differ from the selected point and state producer. `Bytes` is optional; absent means unknown, and zero is explicit. `Reasons` explains unavailability.

| Status | Body and source |
| --- | --- |
| `available` | Nonempty saved comparison; source retained; body contains original bytes. |
| `empty` with source | A retained zero-byte patch; still uses exact-reference download. |
| `empty` without source | Proven equal endpoints or explicit unborn empty state; local empty reader, bytes zero. |
| `unavailable` | Metadata and reasons, nil body, nil operational error. |

For example, the status-specific fields of an available response are `"status":"available","bytes":178,"source":{...}`; a retained empty artifact has `"status":"empty","bytes":0,"source":{...}`; a proven source-less empty comparison has `"status":"empty","bytes":0` and no source; unavailable has `"status":"unavailable","reasons":["selected task boundary was not persisted"]` and neither bytes nor source. Full concrete metadata captured from native fixtures appears in the delivery metadata examples; hashes, artifact keys and observation IDs are not fabricated.

Completed points exclude later retries/producers. Open scopes expose durable progress with `in_progress`; failed recorded after-states use `diagnostic`. Discovery lists scope attempts and endpoint evidence independently of restart eligibility. Missing/conflicting evidence remains unavailable.

An edit can follow a recorded clean state without a saved clean snapshot when native sequence or state-machine execution proves the handoff was serial and the edit's incoming repository, workspace, cell, base, and head match that state. This resolves competing observations; it does not establish a source for patch bytes.

The reader prefers `diff_from_base.diff`. A parent-only file requires recognized version-1 managed producer endpoint proof. A task-context `parent_hash`, a filename or cumulative-file absence cannot provide that proof. Ordinary history-advanced tasks without actual-parent proof remain unavailable. Explicit empty bases retain absent hashes.

Patchless unchanged observers may omit the legacy output parent hash only when
recorded before/after comparisons are identical, fixed resolved-base and incoming
and output heads agree, and the exact incoming snapshot still leads to the original
eligible source. Nonempty invocation metadata must agree with the observed workspace.
An initially empty workspace can later have a materialized active base; entry
provenance does not redefine that base. Any saved-patch artifact or reference,
including malformed references, retains strict producer validation. Matching hashes
without the recorded snapshot link never authorize patch reuse.

Full integrity requires reading to EOF and checking Close. `WorkspaceDiffError` provides version, code, detail and local unwrap cause. Cancellation, deadlines and lease loss preserve recognized identities. Missing/expired bytes are unavailable when confirmed; denial, storage, protocol, transport, count, read and close faults are errors. The authenticated exact-reference route adds completion signaling while preserving existing download callers.

## Backend limits and scope

This implementation uses the existing pinned JobDB artifact interfaces. No cancelled backend expansion is adopted. Its HTTP runtime buffers `OpenArtifact` before returning, so a metadata probe can consume backend bytes. The backend also discards some provider close errors and returns some missing-object failures as untyped storage errors. c2j preserves the failures that reach its public port; it cannot recover errors the backend discards. Such unclassified storage errors remain errors, rather than being guessed to mean unavailable.

No new archive, token, history API, Git execution during reads, snapshot export, composition, producer format, backfill, binary recorder or Cortex UI is introduced. Normal managed review, verification and publication are separate from Cortex adoption.
