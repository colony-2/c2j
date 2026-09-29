# Schema-driven input and review

Status: implemented. See [Structured input and review](GUIDE-Structured-Input-And-Review.md)
for the supported configuration, application API, and first-version limitations.

Extend `input` with a general structured request/response contract. Implement
document review as a preparation extension and reusable recipe, using existing
artifact attachments for both original documents and returned annotations.

## Ownership and execution

```text
review preparation extension → structured input → optional feedback packaging
```

| Component | Responsibility |
|---|---|
| c2j `input` | Publish an immutable request, wait for a response, validate its schema and attachment bindings, and expose the accepted result |
| Review extension | Define the versioned review format; resolve and hash documents; generate request-specific response constraints |
| Reusable recipe | Compose the steps; expose convenient review inputs and outputs; route decisions and enforce workflow gates |
| Consuming application | Present the request, collect responses and attachments, and submit through the shared validated input path |

Use ordinary extension execution for preparation and optional packaging. The
existing input task supplies the durable wait; no new extension suspension
protocol or c2j HTTP service is required. Review behavior can evolve independently
of c2j releases.

## Generic input contract

Add a structured mode alongside existing forms:

- `request`: JSON describing the requested interaction.
- `request_schema`: optional JSON Schema validating that request.
- `response_schema`: required JSON Schema describing acceptable response data.
- `presentation`: optional versioned rendering hints, independent of validation.
- Attachments: existing c2j/JobDB artifacts and artifact references.

Use the existing JSON Schema machinery. Freeze the resolved schemas and request
together when publishing. Runtime-owned request identity identifies the exact
waiting occurrence: replay preserves it; a later invocation gets a new identity.
Schemas describe domain data, while the runtime handles request identity and
submission metadata separately.

The accepted output contains `response`, its durable artifact references, and a
generic receipt identifying the request, submission, recorded actor, and time.
Decision meanings and subject verification remain recipe concerns. Preserve
workspace execution context through input completion.

## Review format and attachments

A review request contains a title, summary, offered decisions, optional immutable
subject/version, and documents keyed by stable IDs. Each document contains a
title, media type, stored artifact reference, exact-byte hash, and annotation
policy. Preserve original producer references, including child-job artifacts.

A response contains a decision, feedback, and annotations keyed by document ID:

```yaml
response:
  decision: revise
  feedback: Explain the recovery behavior.
  annotations:
    design:
      base_sha256: <original document hash>
      format: criticmarkup
      artifact: <existing stored artifact reference to annotated Markdown>
```

Original and annotated documents travel through existing attachment mechanisms.
Reuse task artifacts and stored references, including existing attachment-to-ref
binding conventions; expose those capabilities through structured input wherever
the current input interface does not yet carry them. Accepted responses must
resolve to durable attachments. No inline document bodies, new attachment store,
review-specific upload protocol, or download URLs are part of this contract.

The annotation artifact contains the complete proposed annotated document.
CriticMarkup interpretation belongs to the consuming agent or application;
c2j does not apply edits. An optional packaging extension can create an immutable
feedback object for Codex or similar ops while keeping the decision directly
accessible to recipes. Packaging is not needed to preserve the attachments.

## Validation

The preparation extension reads the originals, verifies availability, hashes
their bytes, and generates a response schema for the particular request:

- Enumerate offered decisions and permitted annotation document IDs.
- Require the matching baseline hash and supported annotation format per file.
- Reject annotations for decisions that accept the reviewed content unchanged.
- Require nonblank feedback or an annotation attachment for revision decisions.
- Use maps keyed by document ID to avoid duplicate annotation entries.

The generic input submission path validates the response and resolves attachment
bindings before completing the wait. Missing, inaccessible, or invalid attachment
bindings leave the request pending, as do schema errors. Schema validation checks
metadata; attachment resolution checks availability. Neither establishes that
feedback is useful or that annotated content preserves the original.

Applications and autofill use this same library path. Storing a schema in JobDB
does not itself enforce it for callers bypassing that path with raw task
completion. Custom executable submission validators are outside the first version.

## Scope and verification

Implement the generic structured input capability first, then the review extension
and reusable recipe. Cover ordinary-form compatibility, generated-schema rules,
attachment persistence and later retrieval, child-produced originals, missing
attachments, replay identity, stale submissions, autofill parity, and input in a
contextualized workspace. Exercise the full path with real JobDB artifacts.

The separate task-completion persistence investigation remains deferred in
[JOBDB_EXTERNAL_TASK_COMPLETION_DISCUSSION.md](JOBDB_EXTERNAL_TASK_COMPLETION_DISCUSSION.md).
This proposal selects no fix for it and makes no additional crash-recovery claim.
