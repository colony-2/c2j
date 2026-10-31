# Reviews for recipe authors

A review is an `input` form with `kind: review`, questions, and optional documents.
Documents use existing stored artifact references. Answers use ordinary form
fields; reviewers can return documents through optional file-upload questions.

No extension, preparation recipe, hashes, or custom JSON Schema is needed.
c2j prepares the form and waits for the response using the input op's internal
substeps. Applications identify reviews through `form.kind` and find their
original documents in `form.documents`.

## Add a review

```yaml
- id: review
  op: input
  inputs:
    form:
      kind: review
      title: Review the design
      documents:
        design: '${{ sequence.draft.artifacts["design.md"] }}'
      fields:
        - id: decision
          type: multiple_choice
          question: How should we proceed?
          required: true
          options:
            - {value: approve, label: Approve}
            - {value: revise, label: Request changes}
        - id: feedback
          type: paragraph_text
          question: What should change?
        - id: annotated_design
          type: file_upload
          question: Optionally attach an annotated design
```

`design` is the review's document ID; `design.md` is the producer's artifact name.
Use `${{ ... }}` to pass artifact references intact. References from child jobs
work too. Do not pass filesystem paths, external URLs, or inline document bodies.
Submitted files can be referenced through `context.artifacts["design.md"]`.

See [the complete example](examples/review/review.yaml). With that file saved as
`review.yaml`, submit it using your configured runtime:

```bash
c2j submit "Review this design" \
  --advanced-recipe-file ./review.yaml \
  --artifact design.md=./design.md
```

Run workers as usual. An application using the c2j input library displays the
review and submits the response. The ordinary interactive CLI prompt does not
render document reviews; `--input-mode ops` exposes the pending form to a client.

## Use answers and returned documents

Read answers from `sequence.review.outputs.fields`:

```yaml
outputs:
  decision: "${{ sequence.review.outputs.fields.decision }}"
  feedback: "${{ sequence.review.outputs.fields.feedback }}"
  receipt: "${{ sequence.review.outputs.receipt }}"
  artifact_refs: "${{ sequence.review.outputs.artifact_refs }}"
```

An answered file-upload question contains a stored artifact reference. On a
branch where `annotated_design` was supplied, bind it into another op's inbox:

```yaml
artifacts:
  annotated-design.md: "${{ states.review.outputs.fields.annotated_design }}"
```

An optional file question can be absent. Before entering that branch, check:

```yaml
when: 'jq(states.review.outputs.fields, ".annotated_design // null") != null'
```

The response's `artifact_refs` map also exposes returned documents by attachment
name. It does not include the original review documents. Original references
remain recorded on the pending form and its persisted preparation outcome.

For a review state, route using ordinary transitions:

```yaml
transitions:
  - to: verify
    when: 'states.review.outputs.fields.decision == "approve"'
  - to: revise
    when: 'states.review.outputs.fields.decision == "revise"'
```

Define those destination states in your state machine. The decision names have
no built-in workflow meaning: approving does not merge, and requesting changes
does not automatically run another op. If an agent revises the documents, pass
its next output artifacts into a new review invocation.

Returned files can be any document format; c2j does not interpret or apply edits.
To continue an agent session, explicitly route its session checkpoint using that
extension's supported input alongside feedback and document attachments.

## Test without waiting for a reviewer

Use ordinary autofill on a test review:

```yaml
form:
  kind: review
  title: Review the proposal
  fields:
    - id: decision
      type: multiple_choice
      question: Proceed?
      required: true
      options:
        - {value: approve, label: Approve}
        - {value: revise, label: Revise}
  autofill:
    fields:
      decision: approve
```

Autofill uses the same review acceptance path and records an automation actor.
File answers must reference real stored artifacts. See the
[review fixture](pkg/input/test-fixtures/recipes/review-input.yaml) and
[integration test](pkg/input/test-fixtures/review_test.go).

Test your answer branches, missing required answers, returned documents, and
text-only responses. Keep production human reviews free of autofill.

## Library integration and limits

Use `ListPendingInputsPage` for paginated occurrence IDs and preparation times,
then `GetForm` or `GetDetails` for the marker, questions, and documents.
`OpenReviewDocument` streams an original from the exact pending review.
Use `SubmitFormResponse` with the form's `request_id`, a submission ID, answers,
and an actor supplied by the application. The
[application guide](GUIDE-Structured-Input-And-Review.md) includes a Go example.

The request ID identifies this occurrence of the review. It survives replay;
a later review receives a different ID. Invalid answers, stale request IDs, and
unavailable response attachments leave the review pending. Receipts identify the
accepted request, submission, actor, and time.

The application provides presentation and authentication. Submission IDs are
correlation identifiers, not an idempotency guarantee. Historical receipt lookup
is not implemented, and the separate
[JobDB completion investigation](JOBDB_EXTERNAL_TASK_COMPLETION_DISCUSSION.md)
remains deferred.

## Optional review document presentation (v1, unreleased)

`documents` remains a map from document ID to the existing stored ArtifactRef.
Two optional sibling fields describe those attachments without changing answers:

```yaml
form:
  kind: review
  question: Proceed?
  type: boolean
  documents:
    brief: '${{ sequence.brief.artifacts["brief.md"] }}'
    design: '${{ sequence.draft.artifacts["design.md"] }}'
  overview_document_id: brief
  document_metadata:
    brief:
      title: Decision brief
      description: Change, evidence, and verification limits.
      role: primary
      order: 0
      source_document_ids: [design]
    design:
      title: Original proposal
      role: supporting
```

An overview is an ordinary attached artifact. It may have no metadata or role.
Every metadata key, overview ID, and source ID must exactly match a key in the
same documents map. IDs are case-sensitive and opaque: there is no trimming,
path normalization, artifact-name matching, or external lookup. Sources declare
provenance; they do not prove accuracy or approval. Cycles between distinct
documents are allowed; consumers must not recursively expand source graphs.
Metadata is untrusted descriptive input and grants no access or authority.

All entry properties are optional. Empty metadata maps and overview strings mean
omission. Empty title falls back to document ID; empty description adds no text.
Role is empty/absent (neutral), `primary`, `supporting`, or `diagnostic`; consumers
do not infer it from filenames or content. Explicit `order: 0` differs from no
order. Bounds are checked after expressions resolve, before publishing a review:

| Property | Bound |
| --- | --- |
| Title | 120 Unicode code points |
| Description | 300 Unicode code points |
| Text encoding | Valid UTF-8; no U+0000–U+001F or U+007F controls |
| Order | Integer from 0 through 1000000, inclusive |
| Source list | At most 128 exact document IDs; no duplicates or self-reference |
| Combined presentation | At most 65536 bytes of compact Go encoding/json for the typed optional fields, omitting absent fields |

The combined bound counts keys, sources, overview, and JSON escaping. It does not
limit document maps, artifact bytes, or answers. Invalid supplied metadata fails
with a field-specific error; c2j does not truncate, prune, normalize, or interpret
text as Markdown/HTML. Nonempty additions require `kind: review`; they are not
structured `presentation` and do not enable structured schemas on reviews.

Consumers initially select the overview, otherwise the first primary document,
otherwise the first document ID in ascending UTF-8 byte order. Display groups are
primary, supporting, diagnostic, then neutral. Within each group, explicit orders
sort numerically before omitted orders; ties use document ID byte order. All
documents remain reachable, including unannotated originals and downloads.
[Serialized integration fixtures](pkg/input/testdata/review-presentation.json)
provide expected selections and grouping for downstream consumers.

c2j freezes annotations with the form and original refs in the existing
`generate_form` outcome. Replay preserves source-list order, request identity,
and original bytes; changing producer values does not edit an existing review.
Use a new review occurrence for changed content. Presentation fields are never
answers, `Output.Metadata`, routing rules, or approval actions.

### Existing jobs and activation

Upgrading must preserve existing queued, executing, pending, recovered, and
completed jobs and reviews. Missing additions remain absent. Existing forms do
not require regeneration, migration, new identities, recipe edits, copied
artifacts, or resubmitted answers. Previously permitted missing legacy identity,
timing, and fallback fields remain permitted. No existing document source or
answer format is deprecated, and no legacy variant removal date is proposed.

**Annotated producer activation remains disabled until release identification.**
The minimum supporting module tag and delivered commit must be published by the
release workflow after this implementation and its acceptance tests are released.
No current tag, inspected baseline commit, or older consumer pin proves support.
This unreleased source does not invent a version or capability-discovery API.
Both executing workers and form-serving c2j integrations must meet that minimum;
Cortex must pin it or a later compatible release before annotated activation.

Use a legacy recipe variant that omits `document_metadata` and
`overview_document_id` entirely on unsupported runtimes. Keep briefs as ordinary
attached documents on that branch. Do not switch to structured input to bypass
version requirements. Existing unannotated reviews continue without a client
upgrade; older strict clients must upgrade or be explicitly excluded before
receiving annotated forms. Tolerant older clients can ignore the additive fields
and continue using the unchanged documents map and answer protocol.

Annotated producers on old runtimes are unsupported: decoding might reject or
silently discard additions. Downgrading after annotated forms have been persisted
may lose metadata; retain a compatible runtime/reader while relying on those
forms. This change does not add historical lookup, pending-list fields, receipt
idempotency, rendering, or automatic approval.
