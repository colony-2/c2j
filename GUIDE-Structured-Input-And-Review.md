# Structured input and document review

`input` supports ordinary forms, document reviews, and generic schema-driven
requests. Reviews add a recognizable marker and stored documents to ordinary
questions and answers. Generic JSON Schema remains available separately.

For installation, complete recipe examples, decision routing, and annotation
handoff, start with the [recipe author's guide](GUIDE-Review-Recipe-Authors.md).

## Recipe contract

Use `form.request`, optional `form.request_schema`, required
`form.response_schema`, and optional `form.presentation`:

```yaml
op: input
inputs:
  form:
    request:
      title: Choose a direction
    response_schema:
      type: object
      required: [decision]
      additionalProperties: false
      properties:
        decision: {enum: [approve, revise]}
    presentation:
      type: my-team.decision/v1
```

Structured mode cannot mix with ordinary question/fields/options/default controls.
Existing ordinary forms retain their behavior. Presentation hints do not affect
validation or select a default response.

The request and schemas are recorded by `generate_form`. A runtime-owned
`request_id` remains stable while that input is pending, including after worker
replacement. A later invocation has a different identity even if its request is
identical. Schemas use JSON Schema draft 2020-12 by default; bundle referenced
definitions in the schema rather than referencing mutable external resources.

Outputs are `response`, `artifact_refs`, and `receipt`. The receipt contains
`request_id`, `submission_id`, `submitted_at`, and `actor: {id, kind}`.
Response values are preserved without ordinary-form defaults or normalization.

## Application integration

The transport-independent Go API is `input.NewRuntime(control, sse)`; `sse` may
be nil. Discover waiting inputs with `ListPendingInputs`, read the frozen form
with `GetForm`, and respond with `SubmitStructuredResponse`:

```go
form, err := runtime.GetForm(ctx, tenantID, jobID)
// Handle err; display form.Request and collect a response.

result, err := runtime.SubmitStructuredResponse(ctx, tenantID, jobID,
    input.StructuredSubmission{
        RequestID:    form.RequestID,
        SubmissionID: submissionID,
        Response: map[string]any{
            "decision": "revise",
        },
    }, input.Actor{ID: authenticatedUserID, Kind: "human"})
```

The embedding application authenticates the actor and supplies it separately
from the response. c2j records it; c2j does not authenticate that identity itself.

Response data accepts JSON values and existing stored artifact references. Go
callers can place `jobdb.Artifact` values in JSON-style `map[string]any` / `[]any`
containers when permitted by the response schema. The runtime snapshots new attachments to temporary
files, replaces their values with stored references, validates the resolved
response, and persists the attachments with the task outcome. Source files are
not modified. Artifact bytes are never encoded into the JSON response.

`StructuredSubmission.ArtifactRefs` also accepts named bindings to already-stored
artifacts. References are checked using the current tenant and retain their
producer job and ordinal. Use distinct attachment binding names; conflicting
names and names reserved by the pending outcome are rejected. External resource
URLs must first be snapshotted as stored artifacts.

Schema errors, stale request identities, and unavailable attachments leave the
input pending. Successful return contains the same output recorded for the op.
The library completes the captured waiting task, preserving workspace identity,
git snapshot state, and execution metadata.

Existing ordinary-form submission helpers reject structured inputs rather than
bypass their contract. Applications should use the library path above; no new
HTTP service or upload protocol is required. Direct raw JobDB task completion
bypasses these c2j checks.

`c2j run --input-mode ops` exposes the structured request in its existing
`input_required` output. The ordinary interactive form prompt leaves structured
input pending and directs the caller to a structured-input client.

`submission_id` is recorded for correlation. This first version does not implement
historical receipt lookup or promise successful retries after acceptance. The
separate [JobDB completion investigation](JOBDB_EXTERNAL_TASK_COMPLETION_DISCUSSION.md)
remains deferred.

## Document reviews

Reviews use the ordinary form model with `kind: review` and `documents`, a map
of document IDs to stored artifact references. Questions use `fields` (or a
single `question`), and answers use the existing `fields`/`response` output.
There is no preparation extension, wrapper recipe, document hashing, or required
annotation format. Do not mix review forms with structured schemas.

`GetForm` exposes `Kind`, `Documents`, `Fields`, and `RequestID`; `GetDetails`
exposes the same information in its form model. The documents retain their
producer references and are checked for availability when the form is prepared.
Applications can recognize a review without inspecting its questions or recipe.

Submit a review through the transport-independent library API:

```go
form, err := runtime.GetForm(ctx, tenantID, jobID)
// Handle err; render form.Fields and form.Documents when form.Kind == "review".
result, err := runtime.SubmitFormResponse(ctx, tenantID, jobID,
    input.FormSubmission{
        RequestID:    form.RequestID,
        SubmissionID: submissionID,
        Fields: map[string]any{
            "decision": "revise",
            "feedback": "Explain the recovery behavior.",
            "annotated_design": jobdb.NewArtifactFromBytes("annotated.md", editedBytes),
        },
    }, input.Actor{ID: authenticatedUserID, Kind: "human"})
```

File-upload fields accept existing stored refs, bare JobDB artifact keys, or
`jobdb.Artifact` values. New artifacts are persisted with the response; the
accepted field value becomes a stored ref. `FormSubmission.ArtifactRefs` accepts
additional named, already-stored documents without a file-upload question.
The result contains ordinary answers, `artifact_refs`, and a receipt. Optional
file questions may be omitted. No decision has intrinsic approval/revision
semantics, and files are not parsed or applied.

Review submissions check request identity, required answers, question IDs, basic
answer types/choices, and attachment availability before finishing the wait.
They reuse ordinary form defaults. This is not an arbitrary response-schema or
review-policy mechanism. The legacy `SubmitResponse` method rejects reviews so
it cannot bypass their request identity and attachment handling.

For recipe examples, see the [author guide](GUIDE-Review-Recipe-Authors.md).

## Paginated pending inputs

Use `ListPendingInputsPage` for a live listing of ordinary, review, and structured
input occurrences:

```go
page, err := runtime.ListPendingInputsPage(ctx, tenantID, input.PendingInputOptions{
    PageSize:  50,
    PageToken: nextPageToken, // empty for the first page
})
// Handle err. Render page.Inputs and retain page.NextPageToken for the next call.
```

Each entry contains `job_id`, `task_ordinal`, optional `request_id`, and optional
`requested_at`. The last field is an RFC 3339 UTC string recorded when the form
is prepared and persisted with that occurrence. It remains stable on replay;
it is not the job creation time or a claim about the exact scheduler transition.
Older forms omit it. Ordinary forms may also omit `request_id`; tenant/job/task
ordinal still identifies their occurrence. `GetDetails().Form.RequestedAt`
exposes the same timestamp; the existing `StartTime` retains its job-time meaning.

Page size defaults to JobDB's default (100) and is capped by JobDB at 200. Follow
`next_page_token` until empty, including after an empty page. Entries completed or
replaced during reading are skipped. The listing is not a historical snapshot;
refresh to discover new occurrences, including another review in the same job.
Do not deduplicate occurrences by job ID alone.

This method reads form data for the requested page to obtain occurrence IDs and
times; it never loads document bytes. It returns no titles, form kinds, document
counts, summaries, or kind filters. Fetch `GetForm` when you need those details.
The legacy `ListPendingInputs` keeps its ID-only response and now follows all
backend pages rather than silently truncating the list.

Forms under recipe or op timeouts use these same APIs. With
`workflow.SWFWorkflowControl`, c2j resolves the prepared form through its recorded
timeout admission checkpoint while retaining the original task's completion
guards. This also supports v0.0.58 histories without rewriting them. After a
worker records terminal timeout, discovery omits the input and submissions
return `ErrInputNotPending`. Passing the deadline alone does not add a new API
rejection rule; an enclosing job deadline may still fail the job when it resumes.

## Open an original review document

```go
doc, err := runtime.OpenReviewDocument(ctx, tenantID, jobID, requestID, "design")
if err != nil {
    return err
}
defer doc.Close()
_, err = io.Copy(destination, doc)
return err
```

`ReviewDocument` is an open stream with `ID`, `Name`, `SizeBytes`, and `Ref`.
A size of -1 means unknown. The reference retains the original producer job and
task ordinal, including child-produced documents. The library checks the pending
review identity and document membership and resolves the artifact in the current
tenant. Callers provide a document ID, never a path, URL, or arbitrary artifact
key. Already-open streams remain readable if the review subsequently completes;
new opens require the exact current pending occurrence. Keep the context alive
while reading and always close the stream.

## Handle errors by type

Use `errors.Is` instead of matching messages:

| Error | Meaning |
|---|---|
| `input.ErrValidation` | Invalid fields, answer values, submission metadata, or attachment references |
| `input.ErrStaleRequest` | The supplied occurrence is no longer the current one |
| `input.ErrInputNotPending` | No accessible pending input exists for the job |
| `input.ErrDocumentNotFound` | The requested document ID is absent from that review |
| `input.ErrArtifactUnavailable` | A document or stored attachment cannot be opened/read; the cause describes the read failure |
| `input.ErrStorage` | Input lookup, persistence, or completion failed |

Use `errors.As` with `input.ValidationError` for `Field`, `FieldID`, and `Required`,
or with `*input.RuntimeError` for `Operation`, `Field`, `FieldID`, and `DocumentID`.
Both preserve wrapped causes. Context cancellation and deadline errors remain
recognizable. Transport status codes and authentication belong to the application.

A completion conflict is classified as no-longer-pending or stale only when the
library can observe that the pending task ended or advanced. Otherwise it remains
a storage error. A storage failure during completion can have an ambiguous
outcome; these errors do not add idempotent retry guarantees or resolve the
separate JobDB completion investigation. Historical lookup remains deferred.

## Autofill and tests

Structured `form.autofill` accepts `response` and `artifact_refs` and runs the same
schema and artifact validation. Its receipt records an automation actor; the
runtime supplies the generated request identity. Do not supply human identity,
legacy fields, metadata, or a receipt in structured autofill.

Run the input and review tests with `go test ./pkg/input/... ./pkg/recipe`.
They cover ordinary forms, generic schemas, reviews, autofill, artifact round
trips, stale responses, SQLite reopen, and a review recipe using another
producer's artifacts in a contextualized cell workspace.
