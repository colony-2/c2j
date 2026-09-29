# Structured input and document review

`input` can publish structured JSON and wait for a response conforming to a
request-specific JSON Schema. Documents and annotations use existing JobDB
artifacts. c2j validates the contract; an extension or recipe defines its meaning.

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
            "feedback": "Explain recovery behavior.",
            "annotations": map[string]any{
                "design": map[string]any{
                    "base_sha256": originalHash,
                    "format":      "criticmarkup",
                    "artifact":    jobdb.NewArtifactFromBytes("design-annotated.md", annotatedBytes),
                },
            },
        },
    }, input.Actor{ID: authenticatedUserID, Kind: "human"})
```

The embedding application authenticates the actor and supplies it separately
from the response. c2j records it; c2j does not authenticate that identity itself.

Response data accepts JSON values and existing stored artifact references. Go
callers can place `jobdb.Artifact` values in JSON-style `map[string]any` / `[]any`
containers as shown above. The runtime snapshots new attachments to temporary
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

## Review extension and reusable recipe

[extensions/review](extensions/review/op.yaml) is a normal Python 3 selector
extension. It reads bound originals, hashes exact bytes, and generates the review
request and response schema. Review concepts are not built into `input`.

[examples/review/review.yaml](examples/review/review.yaml) composes preparation and
input. Its inputs are:

- `spec_json`: JSON text containing a title, decisions, optional summary/subject,
  and optional per-document display/annotation settings. Recipe input schemas
  currently use a string here; the extension itself accepts structured JSON.
- `documents`: an artifact map keyed by review document ID.
- `prepare_selector`: the extension selector, defaulting to `./extensions/review`.

Keep the extension at that path in the recipe repository, or provide a pinned Git
selector when distributing it separately. It requires Python 3.9 or newer.

Example specification:

```json
{
  "title": "Review the design",
  "decisions": {
    "approve": {"label": "Approve", "accepts_reviewed_content": true},
    "revise": {"label": "Request changes", "feedback_required": true}
  },
  "document_options": {
    "evidence": {"media_type": "text/plain", "annotation_policy": "none"}
  }
}
```

Markdown defaults to CriticMarkup annotations; plain text defaults to read-only.
The generated schema rejects unknown decisions/documents, incorrect baseline
hashes, annotations on read-only documents, annotations accompanying acceptance,
and revision responses without nonblank feedback or an annotation attachment.
An attachment's existence does not establish the usefulness of its contents.

The recipe exports the original request, response, receipt, and artifact refs.
Later ops can bind those artifacts normally. A separate packaging extension may
bundle them into an immutable feedback object; packaging is optional and is not
needed to retain the files. Recipes control session continuation, revisions, and
verification/merge gates. Annotation text is feedback, never automatically applied.

## Autofill and tests

Structured `form.autofill` accepts `response` and `artifact_refs` and runs the same
schema and artifact validation. Its receipt records an automation actor; the
runtime supplies the generated request identity. Do not supply human identity,
legacy fields, metadata, or a receipt in structured autofill.

Run the input and review tests with `go test ./pkg/input/... ./pkg/recipe`.
They cover schema rules, ordinary forms, autofill, artifact round trips, stale
responses, SQLite reopen, and the extension/recipe/worker path with a separate
producer's artifacts and a contextualized cell workspace.
